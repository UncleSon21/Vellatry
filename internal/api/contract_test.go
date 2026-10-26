package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/notebook"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/reports"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// The dashboard reads these, and nothing used to check that what it expects is what
// arrives. Two bugs came of that and neither side's tests could have caught either: the
// Search page asked for ?month=2026-10-01 against an api that parses YYYY-MM, and typed
// {month, rows} as a bare array, so "top searches" answered 400 on every load since it
// was written and crashed the page the moment it did not.
//
// The api was right and the component was right. Only the contract between them was
// wrong, so the check belongs between them.

// shape is what the dashboard does with a response: ranges over it, or reads keys off it.
type shape struct {
	path  string
	array bool     // the dashboard types it as T[]
	keys  []string // for an object, the keys it reads
}

// contract is every endpoint the dashboard reads with useApi. Add to it when the
// dashboard does; TestEveryDashboardReadIsCovered fails if you forget.
func contract(ids map[string]string) []shape {
	r := func(s string) string {
		for k, v := range ids {
			s = strings.ReplaceAll(s, "{"+k+"}", v)
		}
		return s
	}
	return []shape{
		{path: "/v1/me", keys: []string{"user_id", "email", "orgs"}},
		{path: "/v1/onboarding", keys: []string{"completed", "topics"}},
		{path: "/v1/settings", keys: []string{"plan", "plan_cap", "daily_answer_budget", "engines"}},
		{path: "/v1/brand", keys: []string{"brand", "competitors"}},
		{path: "/v1/brand/samples", array: true},
		{path: "/v1/connections", keys: []string{"connections", "google_available", "asana_available"}},
		{path: "/v1/topics?status=active", array: true},
		{path: "/v1/keywords/runs?limit=3", array: true},
		{path: "/v1/visibility/performance?from=2026-09-01&to=2026-09-30", keys: []string{"overall", "by_engine", "series", "entities"}},
		{path: "/v1/visibility/blindspots?status=open", array: true},
		{path: "/v1/visibility/sources?from=2026-09-01&to=2026-09-30&limit=50", array: true},
		{path: "/v1/search/overview?from=2026-09-01&to=2026-09-30", keys: []string{"current", "previous", "series"}},
		{path: "/v1/search/queries?month=2026-09&limit=20", keys: []string{"month", "rows"}},
		{path: "/v1/search/pages?month=2026-09&limit=20", keys: []string{"month", "rows"}},
		{path: "/v1/analytics/ai-referrals?from=2026-09-01&to=2026-09-30", keys: []string{"totals"}},
		{path: "/v1/site/summary", keys: []string{"open_by_severity", "fixes_by_status"}},
		{path: "/v1/site/findings?status=open", array: true},
		{path: "/v1/fixes?status=proposed", array: true},
		{path: "/v1/notifications?limit=25", array: true},
		{path: "/v1/destinations", keys: []string{"destinations", "slack_available"}},
		{path: "/v1/watchers", keys: []string{"kinds", "watchers"}},
		{path: "/v1/report-series", keys: []string{"series", "sections"}},
		{path: "/v1/reports?limit=50", array: true},
		{path: r("/v1/reports/{report}"), keys: []string{"id", "title", "status", "snapshot", "unverified"}},
		{path: r("/v1/reports/{report}/questions"), array: true},
		{path: "/v1/hub", keys: []string{"slug", "url", "domains"}},
		{path: "/v1/notebooks", array: true},
		{path: r("/v1/notebooks/{notebook}"), keys: []string{"id", "name", "sources_list", "messages", "notes", "recipes"}},
	}
}

func TestDashboardReadsGetWhatItExpects(t *testing.T) {
	pool := testdb.Pool(t)
	srv, user, ids := contractTenant(t, pool)
	t.Cleanup(srv.Close)

	for _, s := range contract(ids) {
		t.Run(s.path, func(t *testing.T) {
			code, body := user.raw("GET", s.path)
			if code != http.StatusOK {
				t.Fatalf("%d: %s", code, strings.TrimSpace(string(body)))
			}
			var any any
			if err := json.Unmarshal(body, &any); err != nil {
				t.Fatalf("not JSON: %v", err)
			}
			if s.array {
				if _, ok := any.([]interface{}); !ok {
					t.Fatalf("the dashboard ranges over this and would crash on .map: got %.80s", body)
				}
				return
			}
			obj, ok := any.(map[string]interface{})
			if !ok {
				t.Fatalf("the dashboard reads keys off this: got %.80s", body)
			}
			for _, k := range s.keys {
				if _, ok := obj[k]; !ok {
					t.Errorf("no %q; the dashboard reads it. Keys: %v", k, sortedKeys(obj))
				}
			}
		})
	}
}

// TestEveryDashboardReadIsCovered keeps the table honest: a page that starts reading a
// new endpoint fails here until the contract names it, rather than going unchecked.
func TestEveryDashboardReadIsCovered(t *testing.T) {
	web := filepath.Join("..", "..", "web")
	if _, err := os.Stat(web); err != nil {
		t.Skip("no dashboard beside this api")
	}
	use := regexp.MustCompile(`useApi<[^>]+>\(` + "`" + `?'?(/v1/[^` + "`" + `'?,)]+)`)
	found := map[string]bool{}
	err := filepath.Walk(web, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".tsx") || strings.Contains(p, "node_modules") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range use.FindAllStringSubmatch(string(src), -1) {
			found[strings.Split(m[1], "?")[0]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("found no dashboard reads at all; the pattern has stopped matching")
	}
	covered := map[string]bool{}
	for _, s := range contract(map[string]string{"report": "x", "notebook": "x"}) {
		covered[strings.Split(s.path, "?")[0]] = true
	}
	var missing []string
	for p := range found {
		// Paths the dashboard builds from an id come through as a template.
		normal := regexp.MustCompile(`\$\{[^}]+\}`).ReplaceAllString(p, "x")
		if !covered[normal] {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the dashboard reads these and the contract does not check them: %v", missing)
	}
}

func sortedKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// raw returns the body as it arrived, because the shape is the thing being checked.
func (c client) raw(method, path string) (int, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, bytes.NewReader(nil))
	req.Header.Set("X-Dev-Email", c.email)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// contractTenant is an organisation with one of everything the dashboard reads, so each
// endpoint answers with a populated shape rather than an empty default.
func contractTenant(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, client, map[string]string) {
	t.Helper()
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...), Verifier: DevVerifier{},
		Hub: &Hub{Pool: pool, Logger: slog.Default()}, Logger: slog.Default(), CanEmail: true}
	srv := httptest.NewServer(s.Handler())

	stamp := time.Now().Format("150405.000000")
	domain := "contract-" + stamp + ".com"
	user := client{t, srv, "lead-" + stamp + "@" + domain}
	code, body, _ := user.do("POST", "/v1/onboarding", map[string]any{"org_name": "Contract Co",
		"brand": map[string]any{"name": "Contract", "domain": domain}})
	if code != http.StatusCreated {
		t.Fatalf("onboarding: %d %v", code, body)
	}
	org := body["org_id"].(string)

	ids := map[string]string{}
	ctx := context.Background()
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var seriesID string
		if err := tx.QueryRow(ctx, `INSERT INTO report_series (org_id, name, period) VALUES ($1, 'Monthly performance', 'month') RETURNING id::text`, org).Scan(&seriesID); err != nil {
			return err
		}
		sr, err := reports.LoadSeries(ctx, tx, seriesID)
		if err != nil {
			return err
		}
		snap := reports.Snapshot{Version: reports.SnapshotVersion, Org: "Contract Co", Brand: "Contract",
			Period:   reports.Period{Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), Label: "September 2026"},
			Previous: reports.Period{Label: "August 2026"}, Order: []string{reports.SecVisibility},
			Visibility: &reports.VisibilitySection{Answers: 100, Visibility: reports.Pair{Current: fptr(41)}}}
		reportID, _, err := reports.SaveDraft(ctx, tx, org, sr, snap, "", false)
		if err != nil {
			return err
		}
		ids["report"] = reportID
		nb, err := notebook.Create(ctx, tx, org, "Contract notebook", "")
		if err != nil {
			return err
		}
		ids["notebook"] = nb.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The hub is created on demand, and the dashboard reads it on the Reports page.
	if code, _, _ := user.do("GET", "/v1/hub", nil); code != http.StatusOK {
		t.Fatalf("hub: %d", code)
	}
	return srv, user, ids
}

func fptr(v float64) *float64 { return &v }
