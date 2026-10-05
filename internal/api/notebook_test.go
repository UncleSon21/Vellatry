package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/notebook"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// The notebook's api records and hands over: a document is queued to be read, a question
// is queued to be answered, and nothing here waits on a page, a model or an embedding.
func TestNotebookAPI(t *testing.T) {
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
	user := client{t, srv, "lead-" + stamp + "@example.com"}
	code, body, _ := user.do("POST", "/v1/onboarding", map[string]any{"org_name": "Wombat",
		"brand": map[string]any{"name": "Wombat", "domain": "wombat-" + stamp + ".com"}})
	if code != http.StatusCreated {
		t.Fatalf("onboarding: %d %v", code, body)
	}
	org := body["org_id"].(string)

	code, body, _ = user.do("POST", "/v1/notebooks", map[string]any{"name": "Competitor research"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)

	// A pasted source is stored and queued to be read, with no reading done here.
	code, body, _ = user.do("POST", "/v1/notebooks/"+id+"/sources", map[string]any{
		"kind": "text", "text": "# Rival pricing\n\nRival charges $89 a month."})
	if code != http.StatusCreated || body["status"] != "pending" {
		t.Fatalf("pasted source: %d %v", code, body)
	}
	if body["title"] != "Rival pricing" {
		t.Errorf("a pasted source should be named after its first line, got %v", body["title"])
	}
	sourceID := body["id"].(string)

	// A web address is checked for shape only; whether it is safe to connect to is the
	// worker's decision, at connect time.
	if code, body, _ := user.do("POST", "/v1/notebooks/"+id+"/sources", map[string]any{"kind": "url", "url": "not a url"}); code != http.StatusBadRequest {
		t.Errorf("a malformed address: %d %v", code, body)
	}
	if code, _, _ := user.do("POST", "/v1/notebooks/"+id+"/sources", map[string]any{"kind": "url", "url": "file:///etc/passwd"}); code != http.StatusBadRequest {
		t.Errorf("a file:// address was accepted: %d", code)
	}
	if code, _, _ := user.do("POST", "/v1/notebooks/"+id+"/sources", map[string]any{"kind": "text", "text": "  "}); code != http.StatusBadRequest {
		t.Errorf("empty text: %d", code)
	}
	if code, _, _ := user.do("POST", "/v1/notebooks/"+id+"/sources", map[string]any{"kind": "pdf", "text": "x"}); code != http.StatusBadRequest {
		t.Errorf("an unknown kind: %d", code)
	}
	if code, _, _ := user.do("POST", "/v1/notebooks/"+id+"/sources", map[string]any{
		"kind": "text", "text": strings.Repeat("a", notebook.MaxSource+1)}); code != http.StatusBadRequest {
		t.Errorf("an oversize source: %d", code)
	}

	// The worker's job is queued, and is what will read it.
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var queued int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1 AND subject_id = $2`,
			domainevents.NotebookSourceAdded, sourceID).Scan(&queued)
		if queued != 1 {
			t.Errorf("events to read the source = %d", queued)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Asking records the question and hands it over; the api answers nothing itself.
	code, body, _ = user.do("POST", "/v1/notebooks/"+id+"/ask", map[string]any{"question": "What does Rival charge?"})
	if code != http.StatusCreated || body["status"] != "thinking" || body["answer"] != "" {
		t.Fatalf("ask: %d %v", code, body)
	}
	if code, _, _ := user.do("POST", "/v1/notebooks/"+id+"/ask", map[string]any{"question": ""}); code != http.StatusBadRequest {
		t.Errorf("an empty question: %d", code)
	}
	for i := 0; i < notebook.MaxThinking; i++ {
		user.do("POST", "/v1/notebooks/"+id+"/ask", map[string]any{"question": "Another question"})
	}
	if code, _, _ := user.do("POST", "/v1/notebooks/"+id+"/ask", map[string]any{"question": "One more"}); code != http.StatusConflict {
		t.Errorf("past the waiting limit: %d, want a conflict the page can explain", code)
	}

	// One request opens the page: the notebook, its sources and the conversation.
	code, body, _ = user.do("GET", "/v1/notebooks/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("open: %d %v", code, body)
	}
	if len(body["sources_list"].([]any)) != 1 || len(body["messages"].([]any)) != notebook.MaxThinking {
		t.Errorf("the page = %v", body)
	}

	// Renaming, deleting a source, deleting the notebook.
	if code, _, _ := user.do("PATCH", "/v1/notebooks/"+id, map[string]any{"name": "Rival pricing"}); code != http.StatusNoContent {
		t.Errorf("rename: %d", code)
	}
	if code, _, _ := user.do("DELETE", "/v1/notebooks/"+id+"/sources/"+sourceID, nil); code != http.StatusNoContent {
		t.Errorf("delete source: %d", code)
	}
	if code, _, _ := user.do("DELETE", "/v1/notebooks/"+id+"/sources/"+sourceID, nil); code != http.StatusNotFound {
		t.Errorf("deleting a source twice: %d", code)
	}

	// Another organisation cannot see or touch this notebook, even knowing its id.
	other := client{t, srv, "other-" + stamp + "@example.org"}
	if code, body, _ := other.do("POST", "/v1/onboarding", map[string]any{"org_name": "Other",
		"brand": map[string]any{"name": "Other", "domain": "other-" + stamp + ".org"}}); code != http.StatusCreated {
		t.Fatalf("second org: %d %v", code, body)
	}
	if code, _, _ := other.do("GET", "/v1/notebooks/"+id, nil); code != http.StatusNotFound {
		t.Errorf("another organisation opened this notebook: %d", code)
	}
	if code, _, _ := other.do("POST", "/v1/notebooks/"+id+"/ask", map[string]any{"question": "What is in here?"}); code != http.StatusNotFound {
		t.Errorf("another organisation asked of this notebook: %d", code)
	}
	if code, _, list := other.do("GET", "/v1/notebooks", nil); code != http.StatusOK || len(list) != 0 {
		t.Errorf("another organisation's list = %d %v", code, list)
	}

	// Saved outputs: a kept answer, the team's own note, and a recipe to ask again.
	code, body, _ = user.do("POST", "/v1/notebooks/"+id+"/ask", map[string]any{"question": "What is in here?"})
	if code != http.StatusConflict {
		t.Fatalf("expected the waiting limit to still hold: %d %v", code, body)
	}
	var msgID int64
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		msgs, err := notebook.Messages(ctx, tx, id, 10)
		if err != nil {
			return err
		}
		msgID = msgs[0].ID
		return notebook.SaveAnswer(ctx, tx, msgID, "Rival charges $89 a month [1].", "answered",
			[]notebook.Citation{{Marker: 1, Title: "Rival pricing", Text: "Rival charges $89 a month."}}, 0)
	}); err != nil {
		t.Fatal(err)
	}

	code, body, _ = user.do("POST", "/v1/notebooks/"+id+"/notes", map[string]any{"message_id": msgID})
	if code != http.StatusCreated || body["kind"] != "answer" || len(body["citations"].([]any)) != 1 {
		t.Fatalf("keeping an answer: %d %v", code, body)
	}
	noteID := int64(body["id"].(float64))
	// Its words are the notebook's, so they stay; its name is the team's.
	if code, _, _ := user.do("PATCH", fmt.Sprintf("/v1/notebooks/%s/notes/%d", id, noteID),
		map[string]any{"body": "Rival charges $49."}); code != http.StatusConflict {
		t.Errorf("rewriting a kept answer: %d, want a conflict the page can explain", code)
	}
	if code, body, _ := user.do("PATCH", fmt.Sprintf("/v1/notebooks/%s/notes/%d", id, noteID),
		map[string]any{"title": "Rival pricing, August"}); code != http.StatusOK || body["title"] != "Rival pricing, August" {
		t.Errorf("retitling a kept answer: %d %v", code, body)
	}
	if code, body, _ := user.do("POST", "/v1/notebooks/"+id+"/notes",
		map[string]any{"body": "They moved to annual billing in July."}); code != http.StatusCreated || body["kind"] != "written" {
		t.Errorf("writing a note: %d %v", code, body)
	}

	code, body, _ = user.do("POST", "/v1/notebook-recipes", map[string]any{"message_id": msgID, "name": "Competitor pricing"})
	if code != http.StatusCreated || body["question"] == "" {
		t.Fatalf("saving a recipe: %d %v", code, body)
	}
	recipeID := int64(body["id"].(float64))

	// The page carries its notes and the organisation's recipes.
	code, body, _ = user.do("GET", "/v1/notebooks/"+id, nil)
	if code != http.StatusOK || len(body["notes"].([]any)) != 2 || len(body["recipes"].([]any)) != 1 {
		t.Errorf("the page = %v", body)
	}

	// Running a recipe asks its question of this notebook and counts the run.
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		msgs, err := notebook.Messages(ctx, tx, id, 10)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.Status == "thinking" {
				if err := notebook.SaveAnswer(ctx, tx, m.ID, "x", "unanswerable", nil, 0); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, body, _ = user.do("POST", "/v1/notebooks/"+id+"/ask", map[string]any{"recipe_id": recipeID})
	if code != http.StatusCreated || body["question"] != "What does Rival charge?" {
		t.Fatalf("running a recipe should ask its own question of this notebook: %d %v", code, body)
	}
	if code, _, list := user.do("GET", "/v1/notebook-recipes", nil); code != http.StatusOK ||
		list[0].(map[string]any)["runs"].(float64) != 1 {
		t.Errorf("a run was not counted: %v", list)
	}

	// Another organisation cannot see the recipes, or touch a note of this notebook.
	if code, _, list := other.do("GET", "/v1/notebook-recipes", nil); code != http.StatusOK || len(list) != 0 {
		t.Errorf("another organisation's recipes = %v", list)
	}
	if code, _, _ := other.do("DELETE", fmt.Sprintf("/v1/notebooks/%s/notes/%d", id, noteID), nil); code != http.StatusNotFound {
		t.Errorf("another organisation deleted a note: %d", code)
	}
	if code, _, _ := user.do("DELETE", fmt.Sprintf("/v1/notebooks/%s/notes/%d", id, noteID), nil); code != http.StatusNoContent {
		t.Errorf("delete note: %d", code)
	}
	if code, _, _ := user.do("DELETE", fmt.Sprintf("/v1/notebook-recipes/%d", recipeID), nil); code != http.StatusNoContent {
		t.Errorf("delete recipe: %d", code)
	}

	if code, _, _ := user.do("DELETE", "/v1/notebooks/"+id, nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code, _, _ := user.do("GET", "/v1/notebooks/"+id, nil); code != http.StatusNotFound {
		t.Errorf("a deleted notebook: %d", code)
	}
}
