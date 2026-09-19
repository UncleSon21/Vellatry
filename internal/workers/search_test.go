package workers

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"golang.org/x/oauth2"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/google"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/platform/secrets"
	"github.com/UncleSon21/vellatry/internal/testdb"
	"github.com/UncleSon21/vellatry/internal/warehouse"
)

type fakeGoogle struct {
	err       error
	exchanged int
}

func (f *fakeGoogle) Sites(context.Context) ([]google.Site, error) {
	return []google.Site{{URL: "sc-domain:koala.com", Permission: "siteOwner"}}, f.err
}
func (f *fakeGoogle) Properties(context.Context) ([]google.Property, error) {
	return []google.Property{{ID: "properties/42", Name: "koala.com"}}, f.err
}
func (f *fakeGoogle) DayRows(_ context.Context, _ string, day time.Time) ([]google.SearchRow, error) {
	d := day.Format(time.DateOnly)
	return []google.SearchRow{
		{Date: d, Query: "mattress", Page: "https://koala.com/mattress", Country: "aus", Clicks: 10, Impressions: 100, Position: 3},
		{Date: d, Query: "sofa bed", Page: "https://koala.com/sofa", Country: "aus", Clicks: 2, Impressions: 50, Position: 8},
	}, f.err
}
func (f *fakeGoogle) DayTotals(_ context.Context, _ string, from, to time.Time) ([]google.DayTotal, error) {
	var out []google.DayTotal
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		out = append(out, google.DayTotal{Date: d.Format(time.DateOnly), Clicks: 13, Impressions: 200, Position: 4}) // incl. anonymised
	}
	return out, f.err
}
func (f *fakeGoogle) AnalyticsDay(_ context.Context, _ string, day time.Time) ([]google.AnalyticsRow, error) {
	return []google.AnalyticsRow{{Date: day.Format(time.DateOnly), Channel: "Referral", Source: "chatgpt.com", Landing: "/", Sessions: 5, Users: 4}}, f.err
}

func job[T river.JobArgs](args T) *river.Job[T] {
	return &river.Job[T]{JobRow: &rivertype.JobRow{}, Args: args}
}

func TestSearchSync(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "sync")
	box, err := secrets.NewBox("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	fg := &fakeGoogle{}
	s := &Search{
		Pool: pool, Box: box, Warehouse: warehouse.NewMemory(), Logger: slog.Default(),
		Bus:       events.NewBus(inserter, domainevents.Subscriptions()...),
		NewClient: func(context.Context, string) GoogleAPI { return fg },
		Exchange: func(_ context.Context, code string) (*oauth2.Token, error) {
			fg.exchanged++
			if code != "the-code" {
				t.Errorf("exchanged %q", code)
			}
			return &oauth2.Token{RefreshToken: "rt"}, nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC) },
	}
	s.Register(river.NewWorkers())

	sealedCode, _ := box.Seal(org, []byte("the-code"))
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO connections (org_id, kind, status, secret) VALUES ($1, 'google', 'pending', $2)`, org, sealedCode)
		return err
	})

	// Authorise twice: the code is exchanged once; the second run is a no-op.
	auth := &googleAuthorizeWorker{s: s}
	for i := 0; i < 2; i++ {
		if err := auth.Work(ctx, job(jobargs.GoogleAuthorize{OrgID: org})); err != nil {
			t.Fatal(err)
		}
	}
	if fg.exchanged != 1 {
		t.Errorf("code exchanged %d times, want 1", fg.exchanged)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		g, err := loadGrant(ctx, tx)
		if err != nil {
			return err
		}
		rt, err := box.Open(org, g.Secret)
		if g.Status != "connected" || !g.Config.Listed || len(g.Config.Sites) != 1 || string(rt) != "rt" || err != nil {
			t.Errorf("grant = %+v, token %q, %v", g, rt, err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO connections (org_id, kind, status, config) VALUES ($1, 'search_console', 'connected', '{"property":"sc-domain:koala.com"}')`, org)
		return err
	})

	// Sync the same range twice: whole days are replaced, never added to.
	rangeWorker := &searchSyncRangeWorker{s: s}
	for i := 0; i < 2; i++ {
		if err := rangeWorker.Work(ctx, job(jobargs.SearchSyncRange{OrgID: org, From: "2026-09-01", To: "2026-09-03"})); err != nil {
			t.Fatal(err)
		}
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var days int
		var imps int64
		if err := tx.QueryRow(ctx, `SELECT count(*), sum(impressions) FROM search_daily`).Scan(&days, &imps); err != nil {
			return err
		}
		if days != 3 || imps != 600 {
			t.Errorf("search_daily: %d days, %d impressions; want 3 and 600", days, imps)
		}
		var mattress int64
		if err := tx.QueryRow(ctx, `SELECT impressions FROM search_query_monthly WHERE query = 'mattress' AND month = '2026-09-01'`).Scan(&mattress); err != nil {
			return err
		}
		if mattress != 300 {
			t.Errorf("monthly mattress impressions = %d, want 300 (3 days x 100)", mattress)
		}
		var lastDay string
		if err := tx.QueryRow(ctx, `SELECT last_day::text FROM sync_state WHERE kind = 'search_console'`).Scan(&lastDay); err != nil {
			return err
		}
		if lastDay != "2026-09-03" {
			t.Errorf("last_day = %s", lastDay)
		}
		var mismatches int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1`, domainevents.ReconciliationFailed).Scan(&mismatches)
		if mismatches != 0 {
			t.Errorf("reconciliation flagged %d days; detail (150) is below totals (200)", mismatches)
		}
		return err
	})

	// First sync plans the backfill: newest week first, then older weeks.
	chunks, err := s.ranges(ctx, org, "search_console")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 60 || chunks[0][1].Format(time.DateOnly) != "2026-09-19" {
		t.Errorf("backfill chunks = %d, first ends %s", len(chunks), chunks[0][1])
	}
	if again, _ := s.ranges(ctx, org, "search_console"); len(again) != 1 {
		t.Errorf("after backfill, a sync covers the recent days only: %d chunks", len(again))
	}

	// A dead grant breaks the connection visibly and does not retry.
	fg.err = google.ErrAuth
	if err := rangeWorker.Work(ctx, job(jobargs.SearchSyncRange{OrgID: org, From: "2026-09-04", To: "2026-09-04"})); err != nil {
		t.Fatalf("auth failure should not be retried: %v", err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var status string
		var detail *string
		if err := tx.QueryRow(ctx, `SELECT status, status_detail FROM connections WHERE kind = 'google'`).Scan(&status, &detail); err != nil {
			return err
		}
		if status != "broken" || detail == nil {
			t.Errorf("google connection = %s, %v", status, detail)
		}
		return nil
	})

	// Transient failures are returned so River retries them.
	fg.err = errors.Join(google.ErrTransient, errors.New("503"))
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE connections SET status = 'connected' WHERE kind IN ('google', 'search_console')`)
		return err
	})
	if err := rangeWorker.Work(ctx, job(jobargs.SearchSyncRange{OrgID: org, From: "2026-09-04", To: "2026-09-04"})); err == nil {
		t.Error("a transient failure must be retried")
	}
}
