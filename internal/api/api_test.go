package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

type client struct {
	t     *testing.T
	srv   *httptest.Server
	email string
}

func (c client) do(method, path string, body any) (int, map[string]any, []any) {
	c.t.Helper()
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, buf)
	req.Header.Set("X-Dev-Email", c.email)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var obj map[string]any
	var arr []any
	if json.Unmarshal(raw, &obj) != nil {
		_ = json.Unmarshal(raw, &arr)
	}
	return resp.StatusCode, obj, arr
}

func TestAPIEndToEnd(t *testing.T) {
	pool := testdb.Pool(t)
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...), Verifier: DevVerifier{},
		Hub: &Hub{Pool: pool, Logger: slog.Default()}, Logger: slog.Default()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	stamp := time.Now().Format("150405.000000")
	alice := client{t, srv, "alice-" + stamp + "@example.com"}
	bob := client{t, srv, "bob-" + stamp + "@example.com"}

	if code, _, _ := (client{t, srv, ""}).do("GET", "/v1/me", nil); code != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", code)
	}
	if code, _, _ := alice.do("GET", "/v1/brand", nil); code != http.StatusForbidden {
		t.Errorf("before onboarding: %d", code)
	}

	code, body, _ := alice.do("POST", "/v1/onboarding", map[string]any{
		"org_name":    "Koala Pty Ltd",
		"brand":       map[string]any{"name": "Koala", "domain": "https://www.koala.com/au", "exclusions": []string{"koala bear"}},
		"competitors": []map[string]any{{"name": "Ecosa", "domains": []string{"ecosa.com.au"}}},
		"topics":      []map[string]any{{"name": "mattress", "demand_monthly": 5000}},
	})
	if code != http.StatusCreated {
		t.Fatalf("onboarding: %d %v", code, body)
	}
	orgID := body["org_id"].(string)
	if code, _, _ := alice.do("POST", "/v1/onboarding", map[string]any{"org_name": "again", "brand": map[string]any{"name": "x", "domain": "x.com"}}); code != http.StatusConflict {
		t.Errorf("second onboarding: %d", code)
	}

	code, body, _ = alice.do("GET", "/v1/brand", nil)
	if code != http.StatusOK || body["brand"].(map[string]any)["domain"] != "koala.com" {
		t.Errorf("brand: %d %v", code, body)
	}
	code, _, prompts := alice.do("GET", "/v1/prompts", nil)
	if code != http.StatusOK || len(prompts) != 4 {
		t.Fatalf("template prompts: %d, %d prompts", code, len(prompts))
	}
	promptID := prompts[0].(map[string]any)["id"].(string)

	// Onboarding emitted an event that queued discovery for this org.
	var queued int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'visibility_plan_org' AND args->>'org_id' = $1`, orgID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("discovery jobs queued = %d, want 1", queued)
	}

	if code, _, _ := alice.do("POST", "/v1/prompts/"+promptID+"/check", nil); code != http.StatusAccepted {
		t.Errorf("check now: %d", code)
	}
	if code, _, _ := alice.do("PATCH", "/v1/prompts/"+promptID, map[string]string{"status": "tracked"}); code != http.StatusNoContent {
		t.Errorf("track: %d", code)
	}
	if code, _, _ := alice.do("PUT", "/v1/settings", map[string]any{"daily_answer_budget": 5000}); code != http.StatusBadRequest {
		t.Errorf("budget above the plan cap: %d", code)
	}
	if code, _, _ := alice.do("PUT", "/v1/brand", map[string]any{"unknown_field": 1}); code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", code)
	}
	for _, path := range []string{"/v1/visibility/performance", "/v1/visibility/grid", "/v1/visibility/blindspots", "/v1/visibility/sources", "/v1/events", "/v1/topics"} {
		if code, body, _ := alice.do("GET", path, nil); code != http.StatusOK {
			t.Errorf("%s: %d %v", path, code, body)
		}
	}

	// Bob has his own org and cannot reach Alice's data by id or by header.
	bob.do("POST", "/v1/onboarding", map[string]any{"org_name": "Bob Co", "brand": map[string]any{"name": "Bob", "domain": "bob.example"}})
	if code, _, _ := bob.do("GET", "/v1/prompts/"+promptID+"/answers", nil); code != http.StatusNotFound {
		t.Errorf("bob read alice's prompt: %d", code)
	}
	if code, _, _ := bob.do("PATCH", "/v1/prompts/"+promptID, map[string]string{"status": "rejected"}); code != http.StatusNotFound {
		t.Errorf("bob changed alice's prompt: %d", code)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/brand", nil)
	req.Header.Set("X-Dev-Email", bob.email)
	req.Header.Set("X-Org-ID", orgID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bob selected alice's org by header: %d", resp.StatusCode)
	}

	// A viewer (the CMO) reads but cannot change anything.
	cmo := client{t, srv, "cmo-" + stamp + "@example.com"}
	cmo.do("GET", "/v1/me", nil) // creates the user
	err = db.InSystem(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) SELECT $1, id, 'viewer' FROM users WHERE email = $2`, orgID, cmo.email)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := cmo.do("GET", "/v1/visibility/performance", nil); code != http.StatusOK {
		t.Errorf("viewer read: %d", code)
	}
	if code, _, _ := cmo.do("PUT", "/v1/brand", map[string]any{"name": "Hacked"}); code != http.StatusForbidden {
		t.Errorf("viewer edit: %d", code)
	}
}
