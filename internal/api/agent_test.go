package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

func TestAgentAnswersFromStoredData(t *testing.T) {
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

	stamp := time.Now().Format("150405.000000")
	lead := client{t, srv, "lead-" + stamp + "@example.com"}
	code, body, _ := lead.do("POST", "/v1/onboarding", map[string]any{
		"org_name":    "Koala Pty Ltd",
		"brand":       map[string]any{"name": "Koala", "domain": "koala-" + stamp + ".com"},
		"competitors": []map[string]any{{"name": "Ecosa", "domains": []string{"ecosa.com.au"}}},
	})
	if code != http.StatusCreated {
		t.Fatalf("onboarding: %d %v", code, body)
	}
	org := body["org_id"].(string)

	// Four weeks of answers: visibility halves in the second fortnight.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		for i := 1; i <= 56; i++ {
			day := today.AddDate(0, 0, -i)
			mentioned := 12
			if i <= 28 {
				mentioned = 6
			}
			if _, err := tx.Exec(ctx, `INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, method_version)
				VALUES ($1, $2, 'chatgpt', 20, 20, $3, 'vis-1')`, org, day, mentioned); err != nil {
				return err
			}
			for _, e := range []struct {
				name  string
				brand bool
				n     int
			}{{"Koala", true, mentioned}, {"Ecosa", false, 10}} {
				if _, err := tx.Exec(ctx, `INSERT INTO visibility_daily_entities (org_id, day, engine, entity, is_brand, answers, mentions)
					VALUES ($1, $2, 'chatgpt', $3, $4, $5, $5)`, org, day, e.name, e.brand, e.n); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO search_daily (org_id, day, clicks, impressions, position_sum)
				VALUES ($1, $2, $3, 1000, 8000)`, org, day, 100-i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A question the router knows is answered on the spot, from stored rows.
	// No date in the question, so the analysis uses its default four weeks: exactly the
	// half of the seeded data where visibility is 30%, against the half before it.
	code, body, _ = lead.do("POST", "/v1/agent/ask", map[string]any{"question": "Why did our AI visibility drop?"})
	if code != http.StatusOK {
		t.Fatalf("ask: %d %v", code, body)
	}
	if body["status"] != "answered" || body["analysis"] != "visibility_change" {
		t.Fatalf("answer = %v", body)
	}
	answer, _ := body["answer"].(string)
	if !strings.Contains(answer, "AI visibility is 30.0%") || !strings.Contains(answer, "down 30.0 points") ||
		!strings.Contains(answer, "Visibility before: 60.0%") {
		t.Errorf("answer = %q; the numbers must be computed, not guessed", answer)
	}
	bundle, ok := body["bundle"].(map[string]any)
	if !ok || len(bundle["facts"].([]any)) < 3 {
		t.Fatalf("bundle = %v", body["bundle"])
	}
	// Every figure in the answer must be in the evidence: that is the whole contract.
	for _, f := range []string{"30.0%", "60.0%"} {
		found := false
		for _, raw := range bundle["facts"].([]any) {
			if strings.Contains(raw.(map[string]any)["value"].(string), f) {
				found = true
			}
		}
		if !found {
			t.Errorf("figure %s is in the answer but not in the evidence", f)
		}
	}

	// Search Console questions are answered the same way.
	code, body, _ = lead.do("POST", "/v1/agent/ask", map[string]any{"question": "Why did organic traffic change?"})
	if code != http.StatusOK || body["analysis"] != "traffic_change" {
		t.Fatalf("traffic question: %d %v", code, body)
	}
	if a, _ := body["answer"].(string); !strings.Contains(a, "clicks") {
		t.Errorf("traffic answer = %q", a)
	}

	// Anything the router does not recognise waits for the planner instead of guessing.
	code, body, _ = lead.do("POST", "/v1/agent/ask", map[string]any{"question": "Write me a poem about mattresses"})
	if code != http.StatusAccepted || body["status"] != "thinking" {
		t.Fatalf("open-ended question: %d %v", code, body)
	}
	id := int64(body["id"].(float64))
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var queued int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1`, domainevents.AgentQuestionAsked).Scan(&queued)
		if queued != 1 {
			t.Errorf("questions handed to the worker = %d", queued)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	code, body, _ = lead.do("GET", "/v1/agent/questions/"+itoa(int(id)), nil)
	if code != http.StatusOK || body["status"] != "thinking" {
		t.Errorf("question %d = %d %v", id, code, body)
	}

	// An action the agent proposed runs only when it is asked for by name.
	code, body, _ = lead.do("POST", "/v1/agent/actions", map[string]any{"kind": "create_watcher", "params": map[string]string{"kind": "visibility_drop", "points": "10"}})
	if code != http.StatusAccepted {
		t.Fatalf("action: %d %v", code, body)
	}
	if code, body, _ := lead.do("POST", "/v1/agent/actions", map[string]any{"kind": "delete_everything"}); code != http.StatusBadRequest {
		t.Errorf("unknown action: %d %v", code, body)
	}
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM watchers WHERE source = 'agent' AND kind = 'visibility_drop'`).Scan(&n)
		if n != 1 {
			t.Errorf("watchers created by the agent = %d", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, hist, _ := lead.do("GET", "/v1/agent/questions", nil)
	if qs, ok := hist["questions"].([]any); !ok || len(qs) != 3 {
		t.Errorf("history = %v", hist["questions"])
	}
	if analyses, ok := hist["analyses"].([]any); !ok || len(analyses) < 10 {
		t.Errorf("the catalogue should be offered with the history: %v", hist["analyses"])
	}
}
