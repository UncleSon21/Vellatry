package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/embed"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/notebook"
	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/reports"
	"github.com/UncleSon21/vellatry/internal/site"
)

// The notebook's three jobs: read a document, embed its passages, answer a question from
// them. All three are here rather than in the api because all three wait on something
// outside Vellatry — a customer's page, the embedding service, a model.

// Notebooks holds the notebook jobs' dependencies.
type Notebooks struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Logger   *slog.Logger
	Embed    *embed.Client // nil: full-text retrieval only, which still answers
	Answerer Completer     // nil: questions are refused rather than left waiting
	Fetch    *http.Client  // nil: site.PublicOnlyClient, which is the only safe default
}

// Register adds the notebook jobs.
func (n *Notebooks) Register(ws *river.Workers) {
	if n.Fetch == nil {
		// A URL here is typed in by a customer, so it goes through the same guard the
		// crawler uses: no private, loopback or link-local address, checked at connect
		// time so a redirect cannot get around it.
		n.Fetch = site.PublicOnlyClient(20 * time.Second)
	}
	river.AddWorker(ws, &notebookReadWorker{n: n})
	river.AddWorker(ws, &notebookEmbedWorker{n: n})
	river.AddWorker(ws, &notebookAnswerWorker{n: n})
}

// ---- reading a source ------------------------------------------------------------------

type notebookReadWorker struct {
	river.WorkerDefaults[jobargs.NotebookRead]
	n *Notebooks
}

func (w *notebookReadWorker) Timeout(*river.Job[jobargs.NotebookRead]) time.Duration {
	return 2 * time.Minute
}

func (w *notebookReadWorker) Work(ctx context.Context, job *river.Job[jobargs.NotebookRead]) error {
	n, org := w.n, job.Args.OrgID

	var src notebook.Source
	var body string
	if err := db.InTenant(ctx, n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		src, body, err = notebook.LoadSource(ctx, tx, job.Args.SourceID)
		return err
	}); err != nil {
		if errors.Is(err, notebook.ErrNotFound) {
			return nil // deleted while it waited; nothing to read
		}
		return err
	}
	if src.Status == "ready" {
		return nil // already read: a retry of a job that got through
	}

	title, text, err := w.read(ctx, org, src, body)
	if err != nil {
		var refuse refusal
		if !errors.As(err, &refuse) && job.Attempt < job.MaxAttempts {
			return err // a page that is down now may be up in a minute
		}
		why := err.Error()
		if errors.As(err, &refuse) {
			why = refuse.why
		}
		w.n.Logger.InfoContext(ctx, "notebook source not read", "org_id", org, "source_id", src.ID, "why", why)
		return db.InTenant(ctx, n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			return notebook.SourceFailed(ctx, tx, src.ID, why)
		})
	}

	chunks := notebook.Split(text)
	return db.InTenant(ctx, n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := notebook.SourceRead(ctx, tx, org, src.ID, title, text, chunks); err != nil {
			return err
		}
		_, err := n.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.NotebookSourceRead, SubjectID: src.ID, Actor: "worker",
			Payload: map[string]any{"notebook_id": src.Notebook, "chunks": len(chunks)}})
		return err
	})
}

// refusal is a reason not to try again: the document is what it is, and a fourth attempt
// reads the same bytes.
type refusal struct{ why string }

func (r refusal) Error() string { return r.why }

func refuse(format string, args ...any) error { return refusal{fmt.Sprintf(format, args...)} }

// read returns the title and text of a source. Pasted and uploaded text is already here;
// a URL is fetched now, and a report is rendered from the version it was pinned to.
func (w *notebookReadWorker) read(ctx context.Context, org string, src notebook.Source, body string) (title, text string, err error) {
	switch {
	case src.Kind == "report":
		return w.readReport(ctx, org, src)
	case src.Kind != "url":
		text, err = notebook.Read("", []byte(body))
		return src.Title, text, readable(err)
	}
	if src.URL == nil || *src.URL == "" {
		return "", "", refuse("There is no address to read.")
	}

	u, err := url.Parse(*src.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", refuse("That is not a web address we can read.")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return "", "", refuse("That is not a web address we can read.")
	}
	req.Header.Set("User-Agent", site.DefaultUserAgent)
	req.Header.Set("Accept", "text/html,text/plain;q=0.9,*/*;q=0.1")
	resp, err := w.n.Fetch.Do(req)
	if err != nil {
		if errors.Is(err, site.ErrBlockedAddress) {
			return "", "", refuse("That address is not on the public internet, so we did not read it.")
		}
		return "", "", fmt.Errorf("fetching %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		if resp.StatusCode < 500 {
			return "", "", refuse("The page answered %d. Check the address, or paste the text instead.", resp.StatusCode)
		}
		return "", "", fmt.Errorf("fetching %s: status %d", u.Host, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, notebook.MaxSource*4+1))
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", u.Host, err)
	}
	text, err = notebook.Read(resp.Header.Get("Content-Type"), raw)
	if err != nil {
		return "", "", readable(err)
	}
	title = src.Title
	if title == "" {
		if t := notebook.TitleOf(raw); t != "" {
			title = t
		} else {
			title = u.Host + u.Path
		}
	}
	return title, text, nil
}

// readReport renders a published report version as the text a notebook searches. It is
// the same plain-text rendering the report's own summary drafter reads, so the passages a
// notebook cites are the figures the report shows and nothing besides.
//
// Every number in it was computed by code before the report was published. The model
// repeating one here is repeating a measurement, which is the only way a number ever
// reaches a reader in this product.
func (w *notebookReadWorker) readReport(ctx context.Context, org string, src notebook.Source) (title, text string, err error) {
	if src.Report == nil || *src.Report == "" {
		return "", "", refuse("There is no report to read.")
	}
	var v reports.Version
	if err := db.InTenant(ctx, w.n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = reports.LoadVersion(ctx, tx, *src.Report, src.Version)
		return err
	}); err != nil {
		if errors.Is(err, reports.ErrNotFound) {
			return "", "", refuse("That report has been withdrawn, so it is no longer readable.")
		}
		return "", "", err
	}
	body := reports.RenderText(v.Snapshot)
	if s := strings.TrimSpace(v.Summary); s != "" {
		// The team's own summary of the period, which is part of what the report says.
		body = "## Summary\n\n" + s + "\n\n" + body
	}
	title = src.Title
	if title == "" {
		title = v.Title
	}
	return title, body, nil
}

// readable turns the reader's refusals into something the team can act on.
func readable(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, notebook.ErrNotText):
		return refuse("That file is not text. Notebooks read web pages, plain text and Markdown; for a PDF or a document, paste the text.")
	case errors.Is(err, notebook.ErrEmpty):
		return refuse("There is no readable text there. A page that builds itself with JavaScript has none to read; paste the text instead.")
	case errors.Is(err, notebook.ErrTooLarge):
		return refuse("That document is too long for one source. Split it and add the parts separately.")
	}
	return err
}

// ---- embedding passages ------------------------------------------------------------------

type notebookEmbedWorker struct {
	river.WorkerDefaults[jobargs.NotebookEmbed]
	n *Notebooks
}

func (w *notebookEmbedWorker) Timeout(*river.Job[jobargs.NotebookEmbed]) time.Duration {
	return 5 * time.Minute
}

// EmbedPerPass is how many passages one job gives vectors. A long document is finished by
// the next pass rather than by one job running for minutes.
const EmbedPerPass = 400

func (w *notebookEmbedWorker) Work(ctx context.Context, job *river.Job[jobargs.NotebookEmbed]) error {
	n, org := w.n, job.Args.OrgID
	if n.Embed == nil {
		return nil // no embedding service: full text answers alone, and says so nowhere
	}

	var todo []notebook.Passage
	var model string
	if err := db.InTenant(ctx, n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if model, err = notebook.CurrentModel(ctx, tx); err != nil {
			return err
		}
		todo, err = notebook.Unembedded(ctx, tx, model, EmbedPerPass)
		return err
	}); err != nil || len(todo) == 0 {
		return err
	}
	texts := make([]string, len(todo))
	for i, p := range todo {
		texts[i] = p.Text
	}
	res, err := n.Embed.Embed(ctx, texts)
	if err != nil {
		return err
	}
	if len(res.Vectors) != len(todo) {
		return fmt.Errorf("notebook embed: asked for %d vectors, got %d", len(todo), len(res.Vectors))
	}
	if err := db.InTenant(ctx, n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		for i, p := range todo {
			if err := notebook.SaveVector(ctx, tx, p.ID, res.Model, res.Vectors[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	n.Logger.InfoContext(ctx, "notebook passages embedded", "org_id", org, "notebook_id", job.Args.NotebookID,
		"passages", len(todo), "model", res.Model)
	// Come back for the rest rather than hold one job open: either this pass filled up,
	// or the service answered under a different model than the stored passages carry and
	// those are now the stale ones. A first pass is not a model change: before it there
	// was no model to change from.
	if len(todo) == EmbedPerPass || (model != "" && res.Model != model) {
		_, err = river.ClientFromContext[pgx.Tx](ctx).Insert(ctx, jobargs.NotebookEmbed{OrgID: org, NotebookID: job.Args.NotebookID}, nil)
	}
	return err
}

// ---- answering ----------------------------------------------------------------------------

type notebookAnswerWorker struct {
	river.WorkerDefaults[jobargs.NotebookAnswer]
	n *Notebooks
}

func (w *notebookAnswerWorker) Timeout(*river.Job[jobargs.NotebookAnswer]) time.Duration {
	return 2 * time.Minute
}

func (w *notebookAnswerWorker) Work(ctx context.Context, job *river.Job[jobargs.NotebookAnswer]) error {
	err := w.answer(ctx, job)
	if err != nil && job.MaxAttempts > 0 && job.Attempt >= job.MaxAttempts {
		// Nobody will run this again, so the page stops saying "thinking".
		_ = db.InTenant(ctx, w.n.Pool, job.Args.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			return notebook.SaveAnswer(ctx, tx, job.Args.MessageID, "", "failed", nil, 0)
		})
	}
	return err
}

func (w *notebookAnswerWorker) answer(ctx context.Context, job *river.Job[jobargs.NotebookAnswer]) error {
	n, org := w.n, job.Args.OrgID

	var msg notebook.Message
	if err := db.InTenant(ctx, n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		msg, err = notebook.LoadMessage(ctx, tx, job.Args.MessageID)
		return err
	}); err != nil {
		if errors.Is(err, notebook.ErrNotFound) {
			return nil
		}
		return err
	}
	if msg.Status != "thinking" {
		return nil
	}

	save := func(answer, status string, cites []notebook.Citation, dropped int) error {
		return db.InTenant(ctx, n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			return notebook.SaveAnswer(ctx, tx, msg.ID, answer, status, cites, dropped)
		})
	}
	if n.Answerer == nil {
		return save("Answering from documents is not switched on for this account.", "failed", nil, 0)
	}

	// The question's own vector, so retrieval can find a passage that says the same thing
	// in other words. Without an embedding service this is empty and full text answers
	// alone: worse, and still an answer.
	var qv []float32
	var model string
	if n.Embed != nil {
		res, err := n.Embed.Embed(ctx, []string{msg.Question})
		if err != nil {
			n.Logger.WarnContext(ctx, "notebook question not embedded, falling back to full text", "org_id", org, "error", err)
		} else if len(res.Vectors) == 1 {
			qv, model = res.Vectors[0], res.Model
		}
	}

	var passages []notebook.Passage
	var sources int
	if err := db.InTenant(ctx, n.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM notebook_sources WHERE notebook_id::text = $1 AND status = 'ready'`, msg.Notebook).Scan(&sources); err != nil {
			return err
		}
		var err error
		passages, err = notebook.Retrieve(ctx, tx, msg.Notebook, msg.Question, model, qv)
		return err
	}); err != nil {
		return err
	}
	switch {
	case sources == 0:
		return save(notebook.WhenThereAreNoSources, "unanswerable", nil, 0)
	case len(passages) == 0:
		return save(notebook.WhenTheSourcesCannotSay, "unanswerable", nil, 0)
	}

	written, err := w.write(ctx, org, msg.Question, passages)
	if err != nil {
		if errors.Is(err, budget.ErrExceeded) {
			n.Logger.WarnContext(ctx, "notebook question not answered: budget", "org_id", org)
			return save("", "failed", nil, 0)
		}
		return err
	}
	answer, cites, dropped := notebook.Ground(written, passages)
	if dropped > 0 {
		n.Logger.InfoContext(ctx, "notebook answer sentences dropped as ungrounded",
			"org_id", org, "notebook_id", msg.Notebook, "dropped", dropped)
	}
	if answer == "" {
		return save(notebook.WhenTheSourcesCannotSay, "unanswerable", nil, dropped)
	}
	return save(answer, "answered", cites, dropped)
}

// write asks the model, handed the passages and the question and nothing else.
func (w *notebookAnswerWorker) write(ctx context.Context, org, question string, ps []notebook.Passage) (string, error) {
	resp, err := w.n.Answerer.Complete(ctx, gateway.Request{
		Purpose: "notebook_answer", OrgID: org, System: notebook.AnswerPrompt,
		Messages: []gateway.Message{{Role: "user", Content: "<passages>\n" + notebook.Context(ps) + "\n</passages>\n\nQuestion: " + question}},
		Schema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"answer"},
			"properties": map[string]any{"answer": map[string]any{"type": "string"}},
		},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil {
		// A reply we cannot read will not read better on the third attempt, and an
		// ungrounded answer is refused anyway.
		w.n.Logger.WarnContext(ctx, "notebook answer unreadable", "org_id", org, "error", err)
		return "", nil
	}
	return strings.TrimSpace(out.Answer), nil
}
