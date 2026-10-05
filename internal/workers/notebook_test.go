package workers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/embed"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/notebook"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/reports"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// notebookBot answers the notebook's purpose with whatever it is told to say, and keeps
// what it was shown so the test can prove the passages are all it was handed.
type notebookBot struct {
	calls int
	reply string // {phrase} is replaced by the marker of the passage holding that phrase
	saw   string
}

func (b *notebookBot) Complete(_ context.Context, req gateway.Request) (gateway.Response, error) {
	b.calls++
	if req.Purpose != "notebook_answer" {
		return gateway.Response{}, nil
	}
	b.saw = req.Messages[0].Content
	out, _ := json.Marshal(map[string]string{"answer": fillMarkers(b.reply, b.saw)})
	return gateway.Response{Text: string(out)}, nil
}

// fillMarkers replaces {phrase} with the marker of the passage that holds it, the way a
// model reading the numbered passages would cite them. Retrieval decides the order, so a
// test that hard-coded [1] would be testing the ranking and not the grounding.
func fillMarkers(reply, passages string) string {
	blocks := strings.Split(passages, "\n[")
	for {
		open := strings.Index(reply, "{")
		if open < 0 {
			return reply
		}
		close := strings.Index(reply[open:], "}")
		if close < 0 {
			return reply
		}
		phrase := reply[open+1 : open+close]
		marker := "[?]"
		for _, block := range blocks {
			if !strings.Contains(block, phrase) {
				continue
			}
			if n, _, ok := strings.Cut(strings.TrimPrefix(block, "["), "]"); ok {
				marker = "[" + n + "]"
				break
			}
		}
		reply = reply[:open] + marker + reply[open+close+1:]
	}
}

// A team puts a page and some pasted text into a notebook and asks a question of them.
// The page is fetched and chunked, the answer is written from the passages alone, and
// every sentence that cannot be traced to one is dropped before anyone reads it.
func TestNotebookReadsAndAnswers(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "notebook")
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, domainevents.Subscriptions()...)

	// The customer's own page. It carries navigation and a script, which must not end up
	// in the passages: they repeat on every page of a site and would be retrieved instead
	// of the page's own words.
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pricing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Pricing — Rival</title></head><body>
			<nav><a href="/">Home</a></nav>
			<h1>Rival pricing</h1>
			<p>Rival charges $89 a month for the Growth plan, billed annually.</p>
			<p>Their Starter plan is $39 a month and has no phone support.</p>
			<script>analytics()</script></body></html>`))
	}))
	t.Cleanup(page.Close)

	bot := &notebookBot{}
	n := &Notebooks{Pool: pool, Bus: bus, Logger: slog.Default(), Answerer: bot, Fetch: page.Client()}
	n.Register(river.NewWorkers())
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	var nb notebook.Notebook
	var fromWeb, pasted, missing notebook.Source
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if nb, err = notebook.Create(ctx, tx, org, "Competitor research", "lead@example.com"); err != nil {
			return err
		}
		web := notebook.New{Kind: "url", URL: page.URL + "/pricing", By: "lead@example.com"}
		if fromWeb, err = notebook.AddSource(ctx, tx, org, nb.ID, web); err != nil {
			return err
		}
		voice := notebook.New{Kind: "text", Title: "Our brand voice", By: "lead@example.com",
			Body: "We write plainly and never oversell.\n\nOur Growth plan is $79 a month."}
		if pasted, err = notebook.AddSource(ctx, tx, org, nb.ID, voice); err != nil {
			return err
		}
		missing, err = notebook.AddSource(ctx, tx, org, nb.ID, notebook.New{Kind: "url", URL: page.URL + "/gone", By: "lead@example.com"})
		return err
	})

	read := &notebookReadWorker{n: n}
	for _, id := range []string{fromWeb.ID, pasted.ID} {
		if err := read.Work(ctx, job(jobargs.NotebookRead{OrgID: org, SourceID: id})); err != nil {
			t.Fatalf("reading %s: %v", id, err)
		}
	}
	// A page that answers 404 is not worth retrying: the team is told, in their words.
	if err := read.Work(ctx, job(jobargs.NotebookRead{OrgID: org, SourceID: missing.ID})); err != nil {
		t.Fatalf("reading a missing page: %v", err)
	}

	tenant(func(ctx context.Context, tx pgx.Tx) error {
		list, err := notebook.Sources(ctx, tx, nb.ID)
		if err != nil {
			return err
		}
		by := map[string]notebook.Source{}
		for _, s := range list {
			by[s.ID] = s
		}
		web := by[fromWeb.ID]
		if web.Status != "ready" || web.Chunks == 0 || web.Title != "Pricing — Rival" {
			t.Errorf("the fetched page = %+v", web)
		}
		if gone := by[missing.ID]; gone.Status != "failed" || gone.Error == nil || !strings.Contains(*gone.Error, "404") {
			t.Errorf("the missing page = %+v", gone)
		}
		var text string
		if err := tx.QueryRow(ctx, `SELECT string_agg(text, ' ') FROM notebook_chunks WHERE source_id::text = $1`, fromWeb.ID).Scan(&text); err != nil {
			return err
		}
		if !strings.Contains(text, "$89 a month") || strings.Contains(text, "analytics()") || strings.Contains(text, "Home") {
			t.Errorf("the page's passages = %q", text)
		}
		return nil
	})

	// Retrieval with no embedding service: full text alone still finds the passage.
	ask := func(question string) notebook.Message {
		t.Helper()
		var m notebook.Message
		tenant(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			m, err = notebook.Ask(ctx, tx, org, nb.ID, "lead@example.com", question)
			return err
		})
		if err := (&notebookAnswerWorker{n: n}).Work(ctx, job(jobargs.NotebookAnswer{OrgID: org, MessageID: m.ID})); err != nil {
			t.Fatalf("answering %q: %v", question, err)
		}
		tenant(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			m, err = notebook.LoadMessage(ctx, tx, m.ID)
			return err
		})
		return m
	}

	bot.reply = "Rival charges $89 a month for the Growth plan {$89 a month}. Yours is $79 a month {$79 a month}. " +
		"That makes you $10 cheaper {$89 a month}. " + // arithmetic: a figure no passage shows
		"Rival is winning on price overall." // nothing cites it
	m := ask("What do Rival and we charge for the Growth plan?")
	if m.Status != "answered" {
		t.Fatalf("status = %s, answer = %q", m.Status, m.Answer)
	}
	if m.Dropped != 2 {
		t.Errorf("dropped %d sentences, want 2 (the arithmetic and the uncited one); answer %q from %+v", m.Dropped, m.Answer, m.Citations)
	}
	for _, gone := range []string{"$10 cheaper", "winning on price"} {
		if strings.Contains(m.Answer, gone) {
			t.Errorf("the answer still carries %q:\n%s", gone, m.Answer)
		}
	}
	// Both sources were searched, so the answer carries a figure from each.
	if !strings.Contains(m.Answer, "$89 a month") || !strings.Contains(m.Answer, "$79 a month") || len(m.Citations) != 2 {
		t.Errorf("answer = %q, citations = %+v", m.Answer, m.Citations)
	}
	for _, c := range m.Citations {
		if c.Text == "" || c.Title == "" {
			t.Errorf("a citation the reader cannot follow: %+v", c)
		}
	}
	// The model is handed the passages and the question, and nothing else at all.
	if !strings.Contains(bot.saw, "[1] from") || !strings.Contains(bot.saw, "charge for the Growth plan?") {
		t.Errorf("the prompt = %q", bot.saw)
	}

	// Nothing the sources can back is not an answer, and the refusal is in our words.
	bot.reply = "Their head of marketing left in June."
	if m := ask("Who runs their marketing?"); m.Status != "unanswerable" || m.Answer != notebook.WhenTheSourcesCannotSay {
		t.Errorf("an ungrounded answer = %q (%s)", m.Answer, m.Status)
	}
	// A question nothing matches never reaches the model at all.
	before := bot.calls
	if m := ask("zzzqqq unlikely wording"); m.Status != "unanswerable" {
		t.Errorf("a question matching nothing = %q (%s)", m.Answer, m.Status)
	}
	if bot.calls != before {
		t.Error("a question with no passages was still paid for")
	}

	// Re-reading a source replaces its passages rather than adding a second copy.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE notebook_sources SET status = 'pending' WHERE id::text = $1`, fromWeb.ID)
		return err
	})
	if err := read.Work(ctx, job(jobargs.NotebookRead{OrgID: org, SourceID: fromWeb.ID})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var chunks, stored int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM notebook_chunks WHERE source_id::text = $1`, fromWeb.ID).Scan(&chunks); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT chunks FROM notebook_sources WHERE id::text = $1`, fromWeb.ID).Scan(&stored); err != nil {
			return err
		}
		if chunks != stored {
			t.Errorf("re-reading left %d passages for a source that says it has %d", chunks, stored)
		}
		return nil
	})
}

// A published report is the one piece of Vellatry's own data a notebook can hold, and it
// is held as the frozen version it was pinned to.
func TestNotebookReadsAPublishedReport(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "notebook-report")

	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	n := &Notebooks{Pool: pool, Logger: slog.Default(), Bus: events.NewBus(inserter, domainevents.Subscriptions()...)}
	n.Register(river.NewWorkers())
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	snap := reports.Snapshot{Version: reports.SnapshotVersion, Org: "Wombat Pty Ltd", Brand: "Wombat",
		Period:   reports.Period{Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Label: "August 2026"},
		Previous: reports.Period{Label: "July 2026"}, Order: []string{reports.SecVisibility},
		Visibility: &reports.VisibilitySection{Answers: 300, Visibility: reports.Pair{Current: ptrf(41.0), Previous: ptrf(35.5)}}}

	var nb notebook.Notebook
	var reportID string
	var src notebook.Source
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var seriesID string
		if err := tx.QueryRow(ctx, `INSERT INTO report_series (org_id, name, period) VALUES ($1, 'Monthly performance', 'month') RETURNING id::text`, org).Scan(&seriesID); err != nil {
			return err
		}
		sr, err := reports.LoadSeries(ctx, tx, seriesID)
		if err != nil {
			return err
		}
		if reportID, _, err = reports.SaveDraft(ctx, tx, org, sr, snap, "", false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reports SET summary = $2 WHERE id::text = $1`, reportID, "Visibility rose to 41.0%, up 5.5 pts."); err != nil {
			return err
		}
		if _, err := reports.Publish(ctx, tx, org, reportID, "lead"); err != nil {
			return err
		}
		if nb, err = notebook.Create(ctx, tx, org, "The quarter", ""); err != nil {
			return err
		}
		src, err = notebook.AddSource(ctx, tx, org, nb.ID, notebook.New{Kind: "report", Title: "", Report: reportID, Version: 1})
		return err
	})

	if err := (&notebookReadWorker{n: n}).Work(ctx, job(jobargs.NotebookRead{OrgID: org, SourceID: src.ID})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		got, body, err := notebook.LoadSource(ctx, tx, src.ID)
		if err != nil {
			return err
		}
		if got.Status != "ready" || got.Chunks == 0 || got.Title != "Monthly performance: August 2026" {
			t.Errorf("the report source = %+v", got)
		}
		if got.Report == nil || *got.Report != reportID || got.Version != 1 {
			t.Errorf("the version was not pinned: %+v", got)
		}
		// Figures the report shows, and the team's own summary of the period.
		for _, want := range []string{"41.0%", "## AI visibility", "Visibility rose to 41.0%, up 5.5 pts."} {
			if !strings.Contains(body, want) {
				t.Errorf("the report's passages are missing %q:\n%s", want, body)
			}
		}
		// It is searchable beside the team's own documents.
		hits, err := notebook.ByText(ctx, tx, nb.ID, "how did AI visibility move", notebook.Candidates)
		if err != nil {
			return err
		}
		if len(hits) == 0 {
			t.Error("the report was not searchable")
		}
		return nil
	})

	// Withdrawing it leaves the passages exactly as they were: a citation has to keep
	// saying what it said. Only a re-read refuses, and says why.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE reports SET status = 'withdrawn' WHERE id::text = $1`, reportID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE notebook_sources SET status = 'pending' WHERE id::text = $1`, src.ID)
		return err
	})
	if err := (&notebookReadWorker{n: n}).Work(ctx, job(jobargs.NotebookRead{OrgID: org, SourceID: src.ID})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		got, _, err := notebook.LoadSource(ctx, tx, src.ID)
		if err != nil {
			return err
		}
		if got.Status != "failed" || got.Error == nil || !strings.Contains(*got.Error, "withdrawn") {
			t.Errorf("a withdrawn report = %+v", got)
		}
		return nil
	})
}

// With an embedding service the second half of retrieval comes on, and a question worded
// differently from the document still finds it.
func TestNotebookFindsWhatIsSaidInOtherWords(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "notebook-embed")

	// A stand-in for ml/embed: a vector per text, so the test exercises the wiring (the
	// model itself is tested in internal/embed against the real service).
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Texts []string `json:"texts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		out := struct {
			Model   string      `json:"model"`
			Dim     int         `json:"dim"`
			Vectors [][]float32 `json:"vectors"`
		}{Model: "test-embed-1", Dim: 3}
		for _, text := range in.Texts {
			out.Vectors = append(out.Vectors, toyVector(text))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(svc.Close)

	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bot := &notebookBot{reply: "Support answers within one business day {one business day}."}
	n := &Notebooks{Pool: pool, Logger: slog.Default(), Answerer: bot,
		Embed: &embed.Client{URL: svc.URL, HTTP: svc.Client()},
		Bus:   events.NewBus(inserter, domainevents.Subscriptions()...)}
	n.Register(river.NewWorkers())
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	var nb notebook.Notebook
	var src notebook.Source
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if nb, err = notebook.Create(ctx, tx, org, "Support policy", ""); err != nil {
			return err
		}
		src, err = notebook.AddSource(ctx, tx, org, nb.ID, notebook.New{Kind: "text", Title: "Support policy",
			Body: "Support answers within one business day.\n\nEscalations go to the account lead."})
		return err
	})
	if err := (&notebookReadWorker{n: n}).Work(ctx, job(jobargs.NotebookRead{OrgID: org, SourceID: src.ID})); err != nil {
		t.Fatal(err)
	}
	if err := (&notebookEmbedWorker{n: n}).Work(ctx, job(jobargs.NotebookEmbed{OrgID: org, NotebookID: nb.ID})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var embedded int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM notebook_chunks WHERE vector IS NOT NULL AND embed_model = 'test-embed-1'`).Scan(&embedded); err != nil {
			return err
		}
		if embedded == 0 {
			t.Error("no passage was given a vector")
		}
		// Running the pass again embeds nothing: it only ever does what has changed.
		left, err := notebook.Unembedded(ctx, tx, "test-embed-1", 10)
		if err == nil && len(left) != 0 {
			t.Errorf("%d passages still look unembedded", len(left))
		}
		return err
	})

	// The question shares no distinctive word with the document, so full text finds
	// nothing and the vector half is what answers.
	var m notebook.Message
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		m, err = notebook.Ask(ctx, tx, org, nb.ID, "", "support answers business")
		return err
	})
	if err := (&notebookAnswerWorker{n: n}).Work(ctx, job(jobargs.NotebookAnswer{OrgID: org, MessageID: m.ID})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		m, err = notebook.LoadMessage(ctx, tx, m.ID)
		return err
	})
	if m.Status != "answered" || len(m.Citations) != 1 {
		t.Fatalf("answer = %q (%s), citations %+v", m.Answer, m.Status, m.Citations)
	}
}

// toyVector is a deterministic stand-in for a sentence embedding: it is not meaningful,
// only consistent, which is all the wiring needs.
func toyVector(text string) []float32 {
	var v [3]float32
	for i, r := range strings.ToLower(text) {
		v[i%3] += float32(r%17) / 100
	}
	return v[:]
}
