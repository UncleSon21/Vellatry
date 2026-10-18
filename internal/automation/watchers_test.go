package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// A competitor overtaking the brand on a question is the alert a marketing team acts on
// fastest. Overtakes decides it from counted answers and is tested on its own; this is
// the query that counts them, and the rule that a week too thin to mean anything says
// nothing.
func TestCompetitorOvertakeWatcher(t *testing.T) {
	pool := testdb.Pool(t)
	org, brandID := testdb.NewOrg(t, pool, "overtake")
	watcher(t, pool, org, CompetitorOvertakes, `{}`)

	var promptID string
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO prompts (org_id, brand_id, text, source, status) VALUES ($1, $2, 'best mattress australia', 'template', 'tracked')
			RETURNING id::text`, org, brandID).Scan(&promptID)
	})
	// Last week the brand led 3 to 1; this week the competitor leads 4 to 1.
	answer := func(daysAgo int, brandIn bool, competitors []string) {
		t.Helper()
		tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			var taskID, id int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO answer_tasks (org_id, prompt_id, engine, purpose, round, status)
				VALUES ($1, $2, 'chatgpt', 'track', 1, 'done') RETURNING id`, org, promptID).Scan(&taskID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO answers (org_id, prompt_id, task_id, engine, round, collected_at, present, text, sources, fan_out, model, method_version)
				VALUES ($1, $2, $3, 'chatgpt', 1, $4, true, 'an answer', '[]'::jsonb, '{}', 'm', 'v1') RETURNING id`,
				org, promptID, taskID, today.AddDate(0, 0, -daysAgo)).Scan(&id); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO answer_signals (answer_id, org_id, brand_mentioned, brand_count, brand_position, brand_cited,
				    competitors_mentioned, cited_competitors, sources_count, visibility_gap, displacement_gap, detector_version)
				VALUES ($1, $2, $3, 1, 1, false, $4, '{}', 0, false, false, 'd1')`, id, org, brandIn, competitors)
			return err
		})
	}
	for _, a := range []struct {
		daysAgo int
		brand   bool
		comps   []string
	}{
		{12, true, []string{}}, {11, true, []string{"Ecosa"}}, {10, true, []string{}}, {9, false, []string{}}, // before: brand 3, Ecosa 1
		{5, false, []string{"Ecosa"}}, {4, false, []string{"Ecosa"}}, // now: brand 1, Ecosa 4
		{3, true, []string{"Ecosa"}}, {2, false, []string{"Ecosa"}},
	} {
		answer(a.daysAgo, a.brand, a.comps)
	}

	var got []Notification
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		got, err = EvaluateDaily(ctx, tx, today)
		return err
	})
	if len(got) != 1 {
		t.Fatalf("notifications = %+v, want one overtake", got)
	}
	n := got[0]
	if n.Kind != CompetitorOvertakes || !strings.Contains(n.Title, "Ecosa") {
		t.Errorf("notification = %+v, want it to name the competitor", n)
	}
	if !strings.Contains(n.Body, "best mattress australia") {
		t.Errorf("body = %q, want the question it happened on", n.Body)
	}

	// A question with too few answers to compare says nothing, however lopsided.
	thin, thinBrand := testdb.NewOrg(t, pool, "overtake-thin")
	watcher(t, pool, thin, CompetitorOvertakes, `{}`)
	var thinPrompt string
	tenant(t, pool, thin, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO prompts (org_id, brand_id, text, source, status) VALUES ($1, $2, 'best pillow', 'template', 'tracked')
			RETURNING id::text`, thin, thinBrand).Scan(&thinPrompt)
	})
	tenant(t, pool, thin, func(ctx context.Context, tx pgx.Tx) error {
		out, err := EvaluateDaily(ctx, tx, today)
		if err != nil {
			return err
		}
		if len(out) != 0 {
			t.Errorf("a question with no answers raised %+v", out)
		}
		return nil
	})
}

// A broken connection is the team's to fix, and the alert has to say which one and
// where to go. It is also the one alert that is about Vellatry not working.
func TestConnectionBrokenWatcher(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "conn-broken")
	watcher(t, pool, org, ConnectionBroken, `{}`)

	fire := func(payload any) []Notification {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var out []Notification
		tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = EvaluateEvent(ctx, tx, events.Stored{
				OrgID: org, Kind: domainevents.ConnectionBroken, OccurredAt: today, Payload: raw,
			})
			return err
		})
		return out
	}

	got := fire(domainevents.ConnectionPayload{Kind: "google"})
	if len(got) != 1 {
		t.Fatalf("notifications = %+v", got)
	}
	if !strings.Contains(got[0].Title, "reconnecting") || got[0].Link != "/settings/connections" {
		t.Errorf("notification = %+v, want it to point at the connections page", got[0])
	}
	// Twice in a day is once: the same connection breaking again is the same news.
	if again := fire(domainevents.ConnectionPayload{Kind: "google"}); again[0].DedupeKey != got[0].DedupeKey {
		t.Errorf("keys %q and %q, want the same news to merge", got[0].DedupeKey, again[0].DedupeKey)
	}
	// A destination is fixed somewhere else, so it is pointed at somewhere else.
	if slack := fire(domainevents.ConnectionPayload{Kind: "slack"}); slack[0].Link != "/automations/destinations" {
		t.Errorf("a broken Slack destination points at %q", slack[0].Link)
	}
	// The provider's own words are preferred when there are any.
	if detailed := fire(domainevents.ConnectionPayload{Kind: "google", Detail: "The refresh token was revoked."}); !strings.HasPrefix(detailed[0].Body, "The refresh token was revoked.") {
		t.Errorf("body = %q, want the reason the provider gave", detailed[0].Body)
	}
	// An event that says nothing about which connection raises nothing.
	if empty := fire(domainevents.ConnectionPayload{}); len(empty) != 0 {
		t.Errorf("an event naming no connection raised %+v", empty)
	}
}

// A blindspot is only worth an alert once it is confirmed, and only while it is open.
func TestBlindspotConfirmedWatcher(t *testing.T) {
	pool := testdb.Pool(t)
	org, brandID := testdb.NewOrg(t, pool, "blindspot-confirmed")
	watcher(t, pool, org, BlindspotConfirmed, `{}`)

	var promptID string
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE brands SET name = 'Koala' WHERE id = $1`, brandID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO prompts (org_id, brand_id, text, source, status) VALUES ($1, $2, 'best mattress australia', 'template', 'tracked')
			RETURNING id::text`, org, brandID).Scan(&promptID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO blindspots (org_id, prompt_id, engine, kind, status, confirmed, winning_competitor, first_seen, last_seen)
			VALUES ($1, $2, 'chatgpt', 'displacement', 'open', true, 'Ecosa', $3, $3)`, org, promptID, today)
		return err
	})

	fire := func(payload map[string]any) []Notification {
		t.Helper()
		raw, _ := json.Marshal(payload)
		var out []Notification
		tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = EvaluateEvent(ctx, tx, events.Stored{
				OrgID: org, Kind: domainevents.BlindspotOpened, OccurredAt: today, Payload: raw,
			})
			return err
		})
		return out
	}

	confirmed := map[string]any{"prompt_id": promptID, "engine": "chatgpt", "kind": "displacement", "confirmed": true, "competitor": "Ecosa"}
	got := fire(confirmed)
	if len(got) != 1 {
		t.Fatalf("notifications = %+v, want one", got)
	}
	if !strings.Contains(got[0].Body, "Ecosa") || !strings.Contains(got[0].Body, "best mattress australia") {
		t.Errorf("body = %q, want who won and on what question", got[0].Body)
	}

	// Not yet confirmed is not yet news: a blindspot still being screened is a guess.
	provisional := map[string]any{"prompt_id": promptID, "engine": "chatgpt", "kind": "displacement", "confirmed": false}
	if out := fire(provisional); len(out) != 0 {
		t.Errorf("an unconfirmed blindspot raised %+v", out)
	}
	// Once it is closed there is nothing to tell anyone about.
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE blindspots SET status = 'resolved' WHERE prompt_id = $1`, promptID)
		return err
	})
	if out := fire(confirmed); len(out) != 0 {
		t.Errorf("a resolved blindspot raised %+v", out)
	}
}
