package api

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// viewerMay lists the routes that write something and that a viewer may still call, each
// with the reason. Every other write route refuses a viewer, and writeCases sends it one.
var viewerMay = map[string]string{
	"POST /v1/onboarding":    "it creates the caller's own organisation, and refuses anyone who already belongs to one",
	"POST /v1/brand/test":    "it previews unsaved edits against stored answers and saves nothing",
	"POST /v1/events/ticket": "it signs a one-minute ticket to follow the live stream",
	"POST /v1/agent/ask":     "asking is reading; the question is kept as the agent's log",
}

// roleFixture is what the viewer's requests point at: rows that exist, so a route that
// forgot to check the role would change something the test can see.
type roleFixture struct {
	competitor, topic, prompt string
	notebook, source          string
	note, recipe              int64
}

type writeCase struct {
	route string // as registered in Handler: "METHOD pattern"
	path  string
	body  any
}

// writeCases is one request to every write route a viewer must not call. The role is
// checked before anything is read, so the routes without a fixture point at ids no row
// has, and a body is given where an editor's request would be accepted.
func writeCases(f roleFixture) []writeCase {
	const noRow = "1"
	nb := "/v1/notebooks/" + f.notebook
	return []writeCase{
		{"POST /v1/onboarding/complete", "/v1/onboarding/complete", nil},
		{"PUT /v1/brand", "/v1/brand", map[string]any{"name": "Changed by a viewer"}},
		{"POST /v1/competitors", "/v1/competitors", map[string]any{"name": "Added by a viewer"}},
		{"DELETE /v1/competitors/{id}", "/v1/competitors/" + f.competitor, nil},
		{"PUT /v1/settings", "/v1/settings", map[string]any{"daily_answer_budget": 10}},
		{"POST /v1/topics", "/v1/topics", map[string]any{"name": "added by a viewer"}},
		{"PATCH /v1/topics/{id}", "/v1/topics/" + f.topic, map[string]any{"status": "out_of_scope"}},
		{"POST /v1/topics/{id}/merge", "/v1/topics/" + f.topic + "/merge", map[string]any{"into": f.topic}},
		{"POST /v1/prompts", "/v1/prompts", map[string]any{"topic_id": f.topic, "text": "added by a viewer"}},
		{"PATCH /v1/prompts/{id}", "/v1/prompts/" + f.prompt, map[string]any{"status": "rejected"}},
		{"POST /v1/prompts/{id}/check", "/v1/prompts/" + f.prompt + "/check", nil},
		{"PATCH /v1/visibility/blindspots/{id}", "/v1/visibility/blindspots/" + noRow, map[string]any{"status": "dismissed"}},
		{"POST /v1/visibility/blindspots/{id}/asana", "/v1/visibility/blindspots/" + noRow + "/asana", nil},
		{"POST /v1/connections/google/start", "/v1/connections/google/start", nil},
		{"DELETE /v1/connections/google", "/v1/connections/google", nil},
		{"POST /v1/connections/asana/start", "/v1/connections/asana/start", nil},
		{"PUT /v1/connections/asana/project", "/v1/connections/asana/project", nil},
		{"DELETE /v1/connections/asana", "/v1/connections/asana", nil},
		{"PUT /v1/connections/{kind}", "/v1/connections/search_console", nil},
		{"POST /v1/site/crawl", "/v1/site/crawl", nil},
		{"PATCH /v1/site/findings/{id}", "/v1/site/findings/" + noRow, map[string]any{"status": "dismissed"}},
		{"PATCH /v1/fixes/{id}", "/v1/fixes/" + noRow, map[string]any{"status": "sent"}},
		{"POST /v1/fixes/{id}/asana", "/v1/fixes/" + noRow + "/asana", nil},
		{"POST /v1/destinations", "/v1/destinations", nil},
		{"PATCH /v1/destinations/{id}", "/v1/destinations/" + noRow, nil},
		{"DELETE /v1/destinations/{id}", "/v1/destinations/" + noRow, nil},
		{"POST /v1/watchers", "/v1/watchers", nil},
		{"PATCH /v1/watchers/{id}", "/v1/watchers/" + noRow, nil},
		{"DELETE /v1/watchers/{id}", "/v1/watchers/" + noRow, nil},
		{"POST /v1/agent/actions", "/v1/agent/actions", map[string]any{"kind": "create_watcher",
			"params": map[string]string{"kind": "visibility_drop", "points": "10"}}},
		{"POST /v1/keywords/runs", "/v1/keywords/runs", nil},
		{"PATCH /v1/keywords/{keyword}", "/v1/keywords/mattress", nil},
		{"POST /v1/report-series", "/v1/report-series", map[string]any{"name": "Added by a viewer"}},
		{"PATCH /v1/report-series/{id}", "/v1/report-series/" + noRow, nil},
		{"POST /v1/report-series/{id}/drafts", "/v1/report-series/" + noRow + "/drafts", nil},
		{"PATCH /v1/reports/{id}", "/v1/reports/" + noRow, map[string]any{"summary": "Written by a viewer."}},
		{"DELETE /v1/reports/{id}", "/v1/reports/" + noRow, nil},
		{"POST /v1/reports/{id}/publish", "/v1/reports/" + noRow + "/publish", map[string]any{}},
		{"POST /v1/reports/{id}/withdraw", "/v1/reports/" + noRow + "/withdraw", nil},
		{"PATCH /v1/hub", "/v1/hub", nil},
		{"POST /v1/hub/rotate", "/v1/hub/rotate", nil},

		// Notebooks: every request here is one an editor's would succeed with.
		{"POST /v1/notebooks", "/v1/notebooks", map[string]any{"name": "Added by a viewer"}},
		{"PATCH /v1/notebooks/{id}", nb, map[string]any{"name": "Renamed by a viewer"}},
		{"POST /v1/notebooks/{id}/sources", nb + "/sources", map[string]any{"kind": "text", "text": "Pasted by a viewer."}},
		{"POST /v1/notebooks/{id}/ask", nb + "/ask", map[string]any{"question": "What does Rival charge?"}},
		{"POST /v1/notebooks/{id}/notes", nb + "/notes", map[string]any{"body": "Written by a viewer."}},
		{"PATCH /v1/notebooks/{id}/notes/{noteId}", fmt.Sprintf("%s/notes/%d", nb, f.note), map[string]any{"title": "Renamed by a viewer"}},
		{"POST /v1/notebook-recipes", "/v1/notebook-recipes", map[string]any{"name": "Added by a viewer", "question": "What does Rival charge?"}},
		{"DELETE /v1/notebook-recipes/{recipeId}", fmt.Sprintf("/v1/notebook-recipes/%d", f.recipe), nil},
		{"DELETE /v1/notebooks/{id}/notes/{noteId}", fmt.Sprintf("%s/notes/%d", nb, f.note), nil},
		{"DELETE /v1/notebooks/{id}/sources/{sourceId}", nb + "/sources/" + f.source, nil},
		{"DELETE /v1/notebooks/{id}", nb, nil},
	}
}

// TestEveryWriteRouteIsDecided makes adding a route that writes a decision about viewers:
// it is either in writeCases, so a viewer is shown to be refused, or in viewerMay with the
// reason it need not be. http.ServeMux cannot list its routes, so they are read from
// Handler's source.
func TestEveryWriteRouteIsDecided(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		if mux, ok := sel.X.(*ast.Ident); !ok || mux.Name != "authed" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		route, _ := strconv.Unquote(lit.Value)
		if method, _, _ := strings.Cut(route, " "); method != http.MethodGet {
			registered[route] = true
		}
		return true
	})

	decided := map[string]bool{}
	for _, c := range writeCases(roleFixture{}) {
		if decided[c.route] {
			t.Errorf("%s is in writeCases twice", c.route)
		}
		decided[c.route] = true
	}
	for route := range viewerMay {
		if decided[route] {
			t.Errorf("%s is both refused and allowed for viewers", route)
		}
		decided[route] = true
	}
	for route := range registered {
		if !decided[route] {
			t.Errorf("%s writes but nobody decided whether a viewer may call it: add it to writeCases (and call canEdit) or to viewerMay with the reason", route)
		}
	}
	// The other direction also stops this test passing on a scan that found nothing.
	for route := range decided {
		if !registered[route] {
			t.Errorf("%s is listed here but Handler does not register it", route)
		}
	}
}

// TestViewersCannotChangeAnything: a viewer (a CMO, say) reads everything the team sees
// and changes none of it. Every write route refuses them, and afterwards every row the
// organisation owns is exactly as it was.
func TestViewersCannotChangeAnything(t *testing.T) {
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
	owner := client{t, srv, "owner-" + stamp + "@example.com"}
	code, body, _ := owner.do("POST", "/v1/onboarding", map[string]any{"org_name": "Numbat",
		"brand":       map[string]any{"name": "Numbat", "domain": "numbat-" + stamp + ".com"},
		"competitors": []map[string]any{{"name": "Rival"}},
		"topics":      []map[string]any{{"name": "mattress"}}})
	if code != http.StatusCreated {
		t.Fatalf("onboarding: %d %v", code, body)
	}
	org := body["org_id"].(string)

	var f roleFixture
	_, body, _ = owner.do("GET", "/v1/brand", nil)
	f.competitor = body["competitors"].([]any)[0].(map[string]any)["id"].(string)
	_, _, list := owner.do("GET", "/v1/topics", nil)
	f.topic = list[0].(map[string]any)["id"].(string)
	_, _, list = owner.do("GET", "/v1/prompts", nil)
	f.prompt = list[0].(map[string]any)["id"].(string)

	_, body, _ = owner.do("POST", "/v1/notebooks", map[string]any{"name": "Competitor research"})
	f.notebook = body["id"].(string)
	_, body, _ = owner.do("POST", "/v1/notebooks/"+f.notebook+"/sources", map[string]any{"kind": "text", "text": "Rival charges $89 a month."})
	f.source = body["id"].(string)
	_, body, _ = owner.do("POST", "/v1/notebooks/"+f.notebook+"/notes", map[string]any{"body": "They moved to annual billing."})
	f.note = int64(body["id"].(float64))
	_, body, _ = owner.do("POST", "/v1/notebook-recipes", map[string]any{"name": "Pricing", "question": "What does Rival charge?"})
	f.recipe = int64(body["id"].(float64))

	viewer := client{t, srv, "cmo-" + stamp + "@example.com"}
	viewer.do("GET", "/v1/me", nil) // creates the user
	if err := db.InSystem(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) SELECT $1, id, 'viewer' FROM users WHERE email = $2`, org, viewer.email)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	before := orgRows(t, pool, org)
	for _, c := range writeCases(f) {
		method, _, _ := strings.Cut(c.route, " ")
		if code, body, _ := viewer.do(method, c.path, c.body); code != http.StatusForbidden {
			t.Errorf("%s: a viewer got %d %v, want 403", c.route, code, body)
		}
	}
	after := orgRows(t, pool, org)
	for table, rows := range before {
		if after[table] != rows {
			t.Errorf("a viewer's requests changed %s", table)
		}
	}

	// Reading is what a viewer is for.
	for _, path := range []string{"/v1/brand", "/v1/topics", "/v1/notebooks", "/v1/notebooks/" + f.notebook, "/v1/notebook-recipes"} {
		if code, body, _ := viewer.do("GET", path, nil); code != http.StatusOK {
			t.Errorf("viewer read %s: %d %v", path, code, body)
		}
	}
}

// orgRows fingerprints every row an organisation owns, table by table, so a test can tell
// whether anything at all changed. Tenant tables are found by their org_id column.
func orgRows(t *testing.T, pool *pgxpool.Pool, org string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := db.InSystem(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT table_name::text FROM information_schema.columns
			WHERE table_schema = current_schema() AND column_name = 'org_id' ORDER BY 1`)
		if err != nil {
			return err
		}
		tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, table := range tables {
			var sum string
			q := fmt.Sprintf(`SELECT coalesce(md5(string_agg(t::text, ',' ORDER BY t::text)), '') FROM %s t WHERE org_id = $1`,
				pgx.Identifier{table}.Sanitize())
			if err := tx.QueryRow(ctx, q, org).Scan(&sum); err != nil {
				return fmt.Errorf("%s: %w", table, err)
			}
			out[table] = sum
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
