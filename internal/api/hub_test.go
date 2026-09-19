package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/reports"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

func TestReportsHub(t *testing.T) {
	pool := testdb.Pool(t)
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...), Verifier: DevVerifier{},
		Hub: &Hub{Pool: pool, Logger: slog.Default()}, Logger: slog.Default()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	s.HubURL = srv.URL
	ctx := context.Background()

	stamp := time.Now().Format("150405.000000")
	team := client{t, srv, "lead-" + stamp + "@koala-" + stamp + ".com"}
	domain := "koala-" + stamp + ".com"
	code, body, _ := team.do("POST", "/v1/onboarding", map[string]any{"org_name": "Koala Pty Ltd", "brand": map[string]any{"name": "Koala", "domain": domain}})
	if code != http.StatusCreated {
		t.Fatalf("onboarding: %d %v", code, body)
	}
	org := body["org_id"].(string)

	cmo := "cmo@" + domain
	if code, body, _ := team.do("POST", "/v1/report-series", map[string]any{"name": "Monthly performance", "recipients": []string{"stranger@gmail.com"}}); code != http.StatusBadRequest {
		t.Errorf("a recipient outside the company was accepted: %d %v", code, body)
	}
	code, body, _ = team.do("POST", "/v1/report-series", map[string]any{"name": "Monthly performance", "recipients": []string{cmo}})
	if code != http.StatusCreated {
		t.Fatalf("series: %d %v", code, body)
	}
	seriesID := body["id"].(string)

	// A drafted report (what the worker's draft job stores).
	snap := reports.Snapshot{Version: reports.SnapshotVersion, Org: "Koala Pty Ltd", Brand: "Koala",
		Period:   reports.Period{Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Label: "August 2026"},
		Previous: reports.Period{Label: "July 2026"}, Order: []string{reports.SecVisibility},
		Visibility: &reports.VisibilitySection{Answers: 300, Visibility: reports.Pair{Current: ptr(41.0), Previous: ptr(35.5)}}}
	var reportID string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		sr, err := reports.LoadSeries(ctx, tx, seriesID)
		if err != nil {
			return err
		}
		reportID, _, err = reports.SaveDraft(ctx, tx, org, sr, snap, "", false)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// The figure check: a number the report does not show blocks publishing until confirmed.
	team.do("PATCH", "/v1/reports/"+reportID, map[string]any{"summary": "Visibility rose to 41.0%, up 5.5 pts. Sales grew 30%."})
	code, body, _ = team.do("POST", "/v1/reports/"+reportID+"/publish", map[string]any{})
	if code != http.StatusConflict || len(body["unverified"].([]any)) != 1 || body["unverified"].([]any)[0] != "30" {
		t.Fatalf("publish with an invented figure: %d %v", code, body)
	}
	team.do("PATCH", "/v1/reports/"+reportID, map[string]any{"summary": "Visibility rose to 41.0%, up 5.5 pts."})
	if code, body, _ = team.do("POST", "/v1/reports/"+reportID+"/publish", map[string]any{}); code != http.StatusOK || body["version"].(float64) != 1 {
		t.Fatalf("publish: %d %v", code, body)
	}

	code, body, _ = team.do("GET", "/v1/hub", nil)
	if code != http.StatusOK {
		t.Fatalf("hub: %d %v", code, body)
	}
	hubURL := body["url"].(string)
	base := strings.TrimPrefix(hubURL, srv.URL)

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) (int, string, http.Header) {
		t.Helper()
		resp, err := browser.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	post := func(path string, form url.Values, origin string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, page, hdr := get(base)
	if code != http.StatusOK || !strings.Contains(page, "Email me a sign-in link") || hdr.Get("X-Robots-Tag") != "noindex, nofollow" || hdr.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("hub before sign-in: %d %s %v", code, page, hdr)
	}
	if code, _, _ := get(base + "/reports/" + reportID); code != http.StatusSeeOther {
		t.Errorf("a report without a session: %d, want a redirect to sign in", code)
	}
	if code, _, _ := get("/hub/zzzzzzzzzzzzzzzz"); code != http.StatusNotFound {
		t.Errorf("unknown hub: %d", code)
	}
	if _, page := post(base+"/login", url.Values{"email": {"someone@gmail.com"}}, srv.URL); !strings.Contains(page, "Use your work email") {
		t.Errorf("an outside address was not turned away: %s", page)
	}
	if code, _ := post(base+"/login", url.Values{"email": {cmo}}, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-site post: %d", code)
	}
	if _, page := post(base+"/login", url.Values{"email": {cmo}}, srv.URL); !strings.Contains(page, "Check your email") {
		t.Fatalf("sign-in request: %s", page)
	}

	// The worker's side: a token that exists only in the email.
	var token string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var requested int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1 AND payload->>'email' = $2`, domainevents.HubLoginRequested, cmo).Scan(&requested); err != nil {
			return err
		}
		if requested != 1 {
			t.Errorf("sign-in requests recorded = %d", requested)
		}
		var err error
		token, err = reports.CreateLogin(ctx, tx, org, cmo, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if code, page, _ := get(base + "/auth?token=" + token); code != http.StatusOK || !strings.Contains(page, `method="post"`) {
		t.Fatalf("auth page: %d %s", code, page)
	}
	if code, _ := post(base+"/auth", url.Values{"token": {token}}, srv.URL); code != http.StatusSeeOther {
		t.Fatalf("sign in: %d", code)
	}
	if code, page := post(base+"/auth", url.Values{"token": {token}}, srv.URL); code != http.StatusBadRequest || !strings.Contains(page, "expired") {
		t.Errorf("a sign-in link worked twice: %d", code)
	}
	code, page, _ = get(base)
	if code != http.StatusOK || !strings.Contains(page, "Monthly performance: August 2026") || !strings.Contains(page, cmo) {
		t.Fatalf("hub list: %d %s", code, page)
	}
	code, page, hdr = get(base + "/reports/" + reportID)
	if code != http.StatusOK || !strings.Contains(page, "41.0%") || !strings.Contains(page, "Visibility rose to 41.0%") ||
		!strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("report page: %d %v", code, hdr)
	}
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var views int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM report_views WHERE report_id::text = $1 AND viewer = $2 AND format = 'web'`, reportID, cmo).Scan(&views)
		if views != 1 {
			t.Errorf("views logged = %d", views)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := get(base + "/reports/" + reportID + "/pdf"); code != http.StatusNotFound {
		t.Errorf("pdf before rendering: %d", code)
	}

	// Another organisation's hub cannot open this report, even with a valid session there.
	other := client{t, srv, "other-" + stamp + "@example.org"}
	code, body, _ = other.do("POST", "/v1/onboarding", map[string]any{"org_name": "Other", "brand": map[string]any{"name": "Other", "domain": "other-" + stamp + ".org"}})
	if code != http.StatusCreated {
		t.Fatalf("second org: %d %v", code, body)
	}
	_, body, _ = other.do("GET", "/v1/hub", nil)
	otherBase := strings.TrimPrefix(body["url"].(string), srv.URL)
	if code, _, _ := get(otherBase + "/reports/" + reportID); code != http.StatusSeeOther {
		t.Errorf("the session opened another organisation's hub: %d", code)
	}

	// Withdrawing takes the report out of the hub.
	if code, _, _ := team.do("POST", "/v1/reports/"+reportID+"/withdraw", nil); code != http.StatusNoContent {
		t.Errorf("withdraw: %d", code)
	}
	if code, _, _ := get(base + "/reports/" + reportID); code != http.StatusNotFound {
		t.Errorf("withdrawn report still readable: %d", code)
	}

	// Signing out ends the session.
	post(base+"/logout", nil, srv.URL)
	if code, page, _ := get(base); code != http.StatusOK || !strings.Contains(page, "Email me a sign-in link") {
		t.Errorf("after sign-out: %d", code)
	}
}

func ptr(v float64) *float64 { return &v }
