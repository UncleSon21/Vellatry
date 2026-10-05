package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// Merging is what the team does with "looks like a topic you already track": the
// keywords and questions move to the topic they keep, and the duplicate is retired with
// a record of where it went. Nothing is deleted.
func TestMergeTopic(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...), Verifier: DevVerifier{},
		Hub: &Hub{Pool: pool, Logger: slog.Default()}, Logger: slog.Default()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	user := client{t, srv, "merge-" + time.Now().Format("150405.000000") + "@example.com"}

	code, body, _ := user.do("POST", "/v1/onboarding", map[string]any{
		"org_name": "Merge Co", "brand": map[string]any{"name": "Merge", "domain": "merge.example"}})
	if code != http.StatusCreated {
		t.Fatalf("onboarding: %d %v", code, body)
	}
	org := body["org_id"].(string)

	// The team tracks "mattresses"; research proposes "mattress", the same topic in
	// other words, with its own keywords.
	var keep, dupe string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, source, status)
			SELECT org_id, id, 'mattresses', 'manual', 'active' FROM brands RETURNING id::text`).Scan(&keep); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, source, status, similar_to, similar_score)
			SELECT org_id, id, 'mattress', 'keywords', 'proposed', $1::uuid, 0.88 FROM brands RETURNING id::text`, keep).Scan(&dupe); err != nil {
			return err
		}
		for _, k := range []string{"mattress sale", "cheap mattress"} {
			if _, err := tx.Exec(ctx, `INSERT INTO keywords (org_id, keyword, topic_id, source) VALUES ($1, $2, $3::uuid, 'idea')`, org, k, dupe); err != nil {
				return err
			}
		}
		// Both topics ask one question; one pair is worded identically.
		for _, p := range []struct{ topic, text string }{
			{keep, "best mattresses in australia"},
			{dupe, "which mattress has the longest trial"},
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO prompts (org_id, brand_id, topic_id, text, source, status)
				SELECT org_id, id, $1::uuid, $2, 'template', 'tracked' FROM brands`, p.topic, p.text); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	code, body, _ = user.do("POST", "/v1/topics/"+dupe+"/merge", map[string]any{"into": keep})
	if code != http.StatusOK {
		t.Fatalf("merge: %d %v", code, body)
	}
	if body["Keywords"] != float64(2) && body["keywords"] != float64(2) {
		t.Errorf("merge moved %v keywords, want 2", body)
	}

	var keywordsMoved, promptsOnKeep int
	var status string
	var mergedInto, similarTo *string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM keywords WHERE topic_id = $1`, keep).Scan(&keywordsMoved); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM prompts WHERE topic_id = $1`, keep).Scan(&promptsOnKeep); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT status, merged_into::text, similar_to::text FROM topics WHERE id = $1`, dupe).
			Scan(&status, &mergedInto, &similarTo)
	}); err != nil {
		t.Fatal(err)
	}
	if keywordsMoved != 2 {
		t.Errorf("%d keywords on the kept topic, want 2", keywordsMoved)
	}
	if promptsOnKeep != 2 {
		t.Errorf("%d questions on the kept topic, want 2 (its own and the one that moved)", promptsOnKeep)
	}
	if status != "out_of_scope" || mergedInto == nil || *mergedInto != keep {
		t.Errorf("the duplicate is %s, merged into %v; it should be retired and say where it went", status, mergedInto)
	}
	if similarTo != nil {
		t.Error("the suggestion should be cleared once it has been acted on")
	}

	// Refusals: into itself, into something that does not exist, into a retired topic.
	if code, _, _ := user.do("POST", "/v1/topics/"+dupe+"/merge", map[string]any{"into": dupe}); code != http.StatusBadRequest {
		t.Errorf("merging a topic into itself: %d", code)
	}
	if code, _, _ := user.do("POST", "/v1/topics/"+keep+"/merge", map[string]any{"into": "11111111-1111-1111-1111-111111111111"}); code != http.StatusNotFound {
		t.Errorf("merging into a topic that does not exist: %d", code)
	}
	if code, _, _ := user.do("POST", "/v1/topics/"+keep+"/merge", map[string]any{"into": dupe}); code != http.StatusBadRequest {
		t.Errorf("merging into a retired topic: %d", code)
	}
}
