package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/platform/secrets"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

func TestOnboardingWizard(t *testing.T) {
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
	carol := client{t, srv, "carol-" + time.Now().Format("150405.000000") + "@example.com"}

	queued := func(kind, org string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'org_id' = $2`, kind, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Step one: nothing exists yet, then the name and website create the organisation.
	if code, body, _ := carol.do("GET", "/v1/onboarding", nil); code != http.StatusOK || body["org"] != false {
		t.Fatalf("before step one: %d %v", code, body)
	}
	code, body, _ := carol.do("POST", "/v1/onboarding", map[string]any{
		"org_name": "Koala Pty Ltd", "brand": map[string]any{"name": "Koala", "domain": "koala.example"}})
	if code != http.StatusCreated {
		t.Fatalf("step one: %d %v", code, body)
	}
	org := body["org_id"].(string)
	code, body, _ = carol.do("GET", "/v1/onboarding", nil)
	if code != http.StatusOK || body["org"] != true || body["completed"] != false || body["can_edit"] != true {
		t.Fatalf("after step one: %d %v", code, body)
	}
	// The site is read straight away so its suggestions are ready by step two, but
	// keyword research waits for the topics the team chooses.
	if queued("site_crawl", org) != 1 || queued("keyword_run", org) != 0 {
		t.Errorf("after step one: %d crawls, %d keyword runs queued", queued("site_crawl", org), queued("keyword_run", org))
	}

	// Correcting the website reads the new site.
	if code, _, _ := carol.do("PUT", "/v1/brand", map[string]any{"domain": "https://www.koala.com/"}); code != http.StatusOK {
		t.Fatalf("domain fix: %d", code)
	}
	if queued("site_crawl", org) != 2 {
		t.Errorf("crawls after a domain change = %d, want 2", queued("site_crawl", org))
	}

	if code, _, _ := carol.do("POST", "/v1/onboarding/complete", nil); code != http.StatusBadRequest {
		t.Errorf("finishing with no topics: %d", code)
	}

	// The crawl proposed a topic; approving it gives it prompts, or nothing would measure it.
	var topicID string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, source, status)
			SELECT org_id, id, 'Mattresses', 'site', 'proposed' FROM brands RETURNING id::text`).Scan(&topicID)
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := carol.do("PATCH", "/v1/topics/"+topicID, map[string]string{"status": "active"}); code != http.StatusNoContent {
		t.Fatalf("approve topic: %d", code)
	}
	if code, _, prompts := carol.do("GET", "/v1/prompts", nil); code != http.StatusOK || len(prompts) != 4 {
		t.Errorf("prompts after approving a proposed topic: %d, %d", code, len(prompts))
	}

	if code, _, _ := carol.do("POST", "/v1/competitors", map[string]any{"name": "Ecosa"}); code != http.StatusCreated {
		t.Fatalf("competitor: %d", code)
	}

	// Test my setup: unsaved aliases and exclusions, tried against pasted text.
	code, body, _ = carol.do("POST", "/v1/brand/test", map[string]any{
		"text":       "Koala Sleep and Ecosa both sell mattresses. A koala bear sleeps 20 hours; Koala ships fast.",
		"aliases":    []string{"Koala Sleep"},
		"exclusions": []string{"koala bear"},
	})
	if code != http.StatusOK || body["brand"] != float64(2) || body["excluded"] != float64(1) {
		t.Fatalf("test my setup: %d %v", code, body)
	}
	comps := body["competitors"].([]any)
	if len(comps) != 1 || comps[0].(map[string]any)["count"] != float64(1) {
		t.Errorf("competitor mentions = %v", comps)
	}
	var marked []string
	for _, seg := range body["segments"].([]any) {
		if m := seg.(map[string]any); m["entity"] != nil {
			marked = append(marked, m["text"].(string))
		}
	}
	if len(marked) != 4 || marked[0] != "Koala Sleep" || marked[2] != "koala bear" {
		t.Errorf("highlighted = %q", marked)
	}
	// Trying edits saves nothing.
	if _, body, _ := carol.do("GET", "/v1/brand", nil); len(body["brand"].(map[string]any)["aliases"].([]any)) != 0 {
		t.Errorf("a test saved the aliases: %v", body["brand"])
	}
	if code, _, _ := carol.do("POST", "/v1/brand/test", map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("empty test: %d", code)
	}
	if code, _, samples := carol.do("GET", "/v1/brand/samples", nil); code != http.StatusOK || len(samples) != 0 {
		t.Errorf("samples before any answers: %d %v", code, samples)
	}

	// Finishing starts keyword research, once.
	for range 2 {
		code, body, _ = carol.do("POST", "/v1/onboarding/complete", nil)
		if code != http.StatusOK || body["completed"] != true {
			t.Fatalf("finish: %d %v", code, body)
		}
	}
	if queued("keyword_run", org) != 1 {
		t.Errorf("keyword runs after finishing = %d, want 1", queued("keyword_run", org))
	}
	var finished int
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1`, domainevents.OnboardingCompleted).Scan(&finished)
	}); err != nil || finished != 1 {
		t.Errorf("onboarding.completed events = %d (%v)", finished, err)
	}
}

func TestConsentReturnsToTheWizard(t *testing.T) {
	pool := testdb.Pool(t)
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox("ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...), Verifier: DevVerifier{},
		Hub: &Hub{Pool: pool, Logger: slog.Default()}, Logger: slog.Default(), Box: box, AppURL: "https://app.example",
		GoogleOAuth: &oauth2.Config{ClientID: "id", Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.example/auth", TokenURL: "https://accounts.example/token"}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	dave := client{t, srv, "dave-" + time.Now().Format("150405.000000") + "@example.com"}
	if code, _, _ := dave.do("POST", "/v1/onboarding", map[string]any{"org_name": "Dave Co", "brand": map[string]any{"name": "Dave", "domain": "dave.example"}}); code != http.StatusCreated {
		t.Fatalf("onboarding: %d", code)
	}

	if code, _, _ := dave.do("POST", "/v1/connections/google/start?return=https://evil.example", nil); code != http.StatusBadRequest {
		t.Errorf("a return page by URL: %d", code)
	}
	code, body, _ := dave.do("POST", "/v1/connections/google/start?return=onboarding", nil)
	if code != http.StatusOK {
		t.Fatalf("start: %d %v", code, body)
	}
	consent, err := url.Parse(body["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(srv.URL + "/oauth/google/callback?code=abc&state=" + url.QueryEscape(consent.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "https://app.example/onboarding?google=authorized" {
		t.Errorf("callback went to %q", loc)
	}
}
