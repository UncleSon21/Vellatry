package automation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// The daily watchers are what tell a team their visibility fell. Drop and Overtakes
// decide that from two windows and are tested on their own; this is the half that
// builds the windows out of the database, where a date off by one means an alert that
// never comes or one about the wrong week.
//
// today is a Wednesday, so "the last 7 days" and "the 7 before" are both plain
// stretches rather than anything to do with calendar weeks.
var today = time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)

func tenant(t *testing.T, pool *pgxpool.Pool, org string, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(context.Background(), pool, org, fn); err != nil {
		t.Fatal(err)
	}
}

// day records one day of answers on one engine.
func day(t *testing.T, pool *pgxpool.Pool, org string, d time.Time, engine string, answers, mentioned int) {
	t.Helper()
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, position_sum, position_n, sentiment_sum, sentiment_n, method_version)
			VALUES ($1, $2, $3, $4, $4, $5, 0, 0, 0, 0, 'vis-1')`, org, d, engine, answers, mentioned)
		return err
	})
}

func watcher(t *testing.T, pool *pgxpool.Pool, org, kind, params string) {
	t.Helper()
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO watchers (org_id, kind, params, delivery) VALUES ($1, $2, $3::jsonb, 'immediate')`, org, kind, params)
		return err
	})
}

func TestVisibilityDropWatcherReadsTheRightWeeks(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "daily-drop")
	watcher(t, pool, org, VisibilityDrop, `{"points": 10}`)

	// The week before: 8 of 10 answers mentioned the brand, so 80%.
	for i := 14; i > 7; i-- {
		day(t, pool, org, today.AddDate(0, 0, -i), "chatgpt", 10, 8)
	}
	// The last 7 days: 3 of 10, so 30%. A fall of 50 points.
	for i := 7; i > 0; i-- {
		day(t, pool, org, today.AddDate(0, 0, -i), "chatgpt", 10, 3)
	}
	// Today is still being collected, and a day before the window has nothing to do
	// with either week. Neither may move the numbers.
	day(t, pool, org, today, "chatgpt", 10, 10)
	day(t, pool, org, today.AddDate(0, 0, -15), "chatgpt", 10, 10)

	var got []Notification
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		got, err = EvaluateDaily(ctx, tx, today)
		return err
	})
	if len(got) != 1 {
		t.Fatalf("notifications = %+v, want one drop", got)
	}
	n := got[0]
	if !strings.Contains(n.Title, "50.0 points") {
		t.Errorf("title = %q, want a fall of 50.0 points", n.Title)
	}
	if !strings.Contains(n.Body, "30.0%") || !strings.Contains(n.Body, "80.0%") {
		t.Errorf("body = %q, want this week's 30.0%% and last week's 80.0%%", n.Body)
	}
	if n.Severity != "warning" || n.Link == "" || n.Kind != VisibilityDrop {
		t.Errorf("notification = %+v", n)
	}
	// The key carries the week, so a watcher alerts once a week however often it runs.
	if !strings.HasSuffix(n.DedupeKey, "2026-W38") {
		t.Errorf("dedupe key = %q, want it to end with the ISO week", n.DedupeKey)
	}
	same := func(d time.Time) string {
		var out []Notification
		tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = EvaluateDaily(ctx, tx, d)
			return err
		})
		if len(out) == 0 {
			return ""
		}
		return out[0].DedupeKey
	}
	if a, b := same(today), same(today.AddDate(0, 0, 1)); a == "" || a != b {
		t.Errorf("running on consecutive days gave keys %q and %q; the second would alert again", a, b)
	}
}

func TestVisibilityDropStaysQuietWhenItShould(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "daily-quiet")

	// Data first, watcher later: a tenant with no watcher is told nothing, and the
	// evaluation does no work at all.
	for i := 14; i > 7; i-- {
		day(t, pool, org, today.AddDate(0, 0, -i), "chatgpt", 10, 8)
	}
	for i := 7; i > 0; i-- {
		day(t, pool, org, today.AddDate(0, 0, -i), "chatgpt", 10, 3)
	}
	none := func() []Notification {
		var out []Notification
		tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = EvaluateDaily(ctx, tx, today)
			return err
		})
		return out
	}
	if got := none(); len(got) != 0 {
		t.Errorf("a tenant with no watchers was sent %+v", got)
	}

	// A watcher that wants a bigger fall than happened says nothing.
	watcher(t, pool, org, VisibilityDrop, `{"points": 60}`)
	if got := none(); len(got) != 0 {
		t.Errorf("a 50 point fall tripped a 60 point watcher: %+v", got)
	}
}

func TestVisibilityDropNeedsEnoughAnswers(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "daily-thin")
	watcher(t, pool, org, VisibilityDrop, `{"points": 10}`)

	// One answer each week, mentioned then not: a 100 point fall on nothing at all.
	day(t, pool, org, today.AddDate(0, 0, -10), "chatgpt", 1, 1)
	day(t, pool, org, today.AddDate(0, 0, -3), "chatgpt", 1, 0)

	var got []Notification
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		got, err = EvaluateDaily(ctx, tx, today)
		return err
	})
	if len(got) != 0 {
		t.Errorf("two answers were enough to raise %+v; a week that thin says nothing", got)
	}
}

// A watcher set to one engine watches that engine, not the total across all of them.
func TestVisibilityDropPerEngine(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "daily-engine")
	watcher(t, pool, org, VisibilityDrop, `{"points": 10, "engine": "gemini"}`)

	for i := 14; i > 7; i-- {
		day(t, pool, org, today.AddDate(0, 0, -i), "chatgpt", 10, 2)
		day(t, pool, org, today.AddDate(0, 0, -i), "gemini", 10, 9)
	}
	for i := 7; i > 0; i-- {
		// ChatGPT improved sharply; Gemini fell. Only Gemini is being watched.
		day(t, pool, org, today.AddDate(0, 0, -i), "chatgpt", 10, 9)
		day(t, pool, org, today.AddDate(0, 0, -i), "gemini", 10, 4)
	}

	var got []Notification
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		got, err = EvaluateDaily(ctx, tx, today)
		return err
	})
	if len(got) != 1 {
		t.Fatalf("notifications = %+v, want one about Gemini", got)
	}
	if !strings.Contains(got[0].Title, "50.0 points") {
		t.Errorf("title = %q; the other engine's rise should not be in it", got[0].Title)
	}
	if eng, _ := got[0].Data.(map[string]any)["engine"].(string); eng != "gemini" {
		t.Errorf("the notification is about %q", eng)
	}
}
