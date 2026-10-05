package notebook

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned for a notebook, source or message that does not exist in this
// organisation.
var ErrNotFound = errors.New("notebook: not found")

// What a reader may ask of one notebook at a time. The limit is what stops a stuck page
// from queueing paid work without end; it is per notebook because that is where the
// person is looking.
const MaxThinking = 3

// ErrAnswersWaiting is returned when a notebook already has questions waiting.
var ErrAnswersWaiting = errors.New("notebook: too many questions waiting")

// MaxQuestion is as long as a question may be.
const MaxQuestion = 500

// Notebook is one place of work: a name and the sources under it.
type Notebook struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedBy *string   `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Sources   int       `json:"sources"`
	Ready     int       `json:"ready"`
}

// Source is one document in a notebook.
type Source struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"` // url | text | file
	Title    string     `json:"title"`
	URL      *string    `json:"url"`
	Bytes    int        `json:"bytes"`
	Chunks   int        `json:"chunks"`
	Status   string     `json:"status"`
	Error    *string    `json:"error"`
	AddedBy  *string    `json:"added_by"`
	AddedAt  time.Time  `json:"added_at"`
	ReadyAt  *time.Time `json:"ready_at"`
	Notebook string     `json:"notebook_id"`
}

// Message is one question and the answer it got.
type Message struct {
	ID         int64      `json:"id"`
	Notebook   string     `json:"notebook_id"`
	AskedBy    string     `json:"asked_by"`
	Question   string     `json:"question"`
	Answer     string     `json:"answer"`
	Status     string     `json:"status"`
	Citations  []Citation `json:"citations"`
	Dropped    int        `json:"dropped"`
	AskedAt    time.Time  `json:"asked_at"`
	AnsweredAt *time.Time `json:"answered_at"`
}

// Citation is a passage an answer used, as the reader sees it.
type Citation struct {
	Marker  int    `json:"marker"` // the [n] in the answer
	ChunkID int64  `json:"chunk_id"`
	Source  string `json:"source_id"`
	Title   string `json:"title"`
	URL     string `json:"url,omitempty"`
	Seq     int    `json:"seq"` // which passage of that source
	Text    string `json:"text"`
}

// ---- notebooks -----------------------------------------------------------------------

// Create makes a notebook. tx must be scoped to the org.
func Create(ctx context.Context, tx pgx.Tx, org, name, by string) (Notebook, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "Untitled notebook"
	}
	if utf8.RuneCountInString(name) > 120 {
		return Notebook{}, errors.New("notebook: the name is too long")
	}
	var n Notebook
	err := tx.QueryRow(ctx, `
		INSERT INTO notebooks (org_id, name, created_by) VALUES ($1, $2, nullif($3, ''))
		RETURNING id::text, name, created_by, created_at, updated_at`, org, name, by).
		Scan(&n.ID, &n.Name, &n.CreatedBy, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

const notebookCols = `n.id::text, n.name, n.created_by, n.created_at, n.updated_at,
	(SELECT count(*) FROM notebook_sources s WHERE s.notebook_id = n.id),
	(SELECT count(*) FROM notebook_sources s WHERE s.notebook_id = n.id AND s.status = 'ready')`

func scanNotebook(r pgx.Row) (Notebook, error) {
	var n Notebook
	err := r.Scan(&n.ID, &n.Name, &n.CreatedBy, &n.CreatedAt, &n.UpdatedAt, &n.Sources, &n.Ready)
	return n, err
}

// List returns the organisation's notebooks, newest first.
func List(ctx context.Context, tx pgx.Tx, limit int) ([]Notebook, error) {
	rows, err := tx.Query(ctx, `SELECT `+notebookCols+` FROM notebooks n ORDER BY n.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Notebook, error) { return scanNotebook(r) })
}

// Load reads one notebook.
func Load(ctx context.Context, tx pgx.Tx, id string) (Notebook, error) {
	n, err := scanNotebook(tx.QueryRow(ctx, `SELECT `+notebookCols+` FROM notebooks n WHERE n.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// Rename changes a notebook's name.
func Rename(ctx context.Context, tx pgx.Tx, id, name string) error {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return errors.New("notebook: the name is empty")
	}
	tag, err := tx.Exec(ctx, `UPDATE notebooks SET name = $2, updated_at = now() WHERE id::text = $1`, id, name)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Delete removes a notebook and everything under it.
func Delete(ctx context.Context, tx pgx.Tx, id string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM notebooks WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- sources -------------------------------------------------------------------------

const sourceCols = `id::text, notebook_id::text, kind, title, url, bytes, chunks, status, error, added_by, added_at, ready_at`

func scanSource(r pgx.Row) (Source, error) {
	var s Source
	err := r.Scan(&s.ID, &s.Notebook, &s.Kind, &s.Title, &s.URL, &s.Bytes, &s.Chunks, &s.Status, &s.Error, &s.AddedBy, &s.AddedAt, &s.ReadyAt)
	return s, err
}

// New is a document being added. A url source is fetched by the worker; a text or file
// source already has its body.
type New struct {
	Kind  string // url | text | file
	Title string
	URL   string
	Body  string
	By    string
}

// AddSource records a document waiting to be read.
func AddSource(ctx context.Context, tx pgx.Tx, org, notebookID string, in New) (Source, error) {
	if _, err := Load(ctx, tx, notebookID); err != nil {
		return Source{}, err
	}
	title := strings.Join(strings.Fields(in.Title), " ")
	if utf8.RuneCountInString(title) > 200 {
		title = string([]rune(title)[:200])
	}
	s, err := scanSource(tx.QueryRow(ctx, `
		INSERT INTO notebook_sources (org_id, notebook_id, kind, title, url, body, bytes, added_by)
		VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, nullif($8, ''))
		RETURNING `+sourceCols, org, notebookID, in.Kind, title, in.URL, in.Body, len(in.Body), in.By))
	return s, err
}

// Sources lists a notebook's documents, oldest first: the order the team added them.
func Sources(ctx context.Context, tx pgx.Tx, notebookID string) ([]Source, error) {
	rows, err := tx.Query(ctx, `SELECT `+sourceCols+` FROM notebook_sources WHERE notebook_id::text = $1 ORDER BY added_at`, notebookID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Source, error) { return scanSource(r) })
}

// LoadSource reads one source with its text.
func LoadSource(ctx context.Context, tx pgx.Tx, id string) (Source, string, error) {
	var s Source
	var body string
	err := tx.QueryRow(ctx, `SELECT `+sourceCols+`, body FROM notebook_sources WHERE id::text = $1`, id).
		Scan(&s.ID, &s.Notebook, &s.Kind, &s.Title, &s.URL, &s.Bytes, &s.Chunks, &s.Status, &s.Error, &s.AddedBy, &s.AddedAt, &s.ReadyAt, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, "", ErrNotFound
	}
	return s, body, err
}

// SourceRead stores what was read from a source, replacing its passages. Replacing
// rather than adding is what makes re-reading a page safe to run twice.
func SourceRead(ctx context.Context, tx pgx.Tx, org, sourceID, title, body string, chunks []Chunk) error {
	var notebookID string
	if err := tx.QueryRow(ctx, `SELECT notebook_id::text FROM notebook_sources WHERE id::text = $1`, sourceID).Scan(&notebookID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM notebook_chunks WHERE source_id::text = $1`, sourceID); err != nil {
		return err
	}
	// One statement rather than COPY: Postgres refuses COPY FROM into a table with
	// row-level security, which every tenant table here has.
	if len(chunks) > 0 {
		seqs := make([]int32, len(chunks))
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			seqs[i], texts[i] = int32(c.Seq), c.Text
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO notebook_chunks (org_id, notebook_id, source_id, seq, text)
			SELECT $1, $2, $3, seq, text FROM unnest($4::int[], $5::text[]) AS p(seq, text)`,
			org, notebookID, sourceID, seqs, texts); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `
		UPDATE notebook_sources SET title = coalesce(nullif($2, ''), title), body = $3, bytes = $4, chunks = $5,
		    status = 'ready', error = NULL, ready_at = now() WHERE id::text = $1`,
		sourceID, title, body, len(body), len(chunks))
	return err
}

// SourceFailed records why a document could not be read, in words the team can act on.
func SourceFailed(ctx context.Context, tx pgx.Tx, sourceID, why string) error {
	_, err := tx.Exec(ctx, `UPDATE notebook_sources SET status = 'failed', error = $2, chunks = 0 WHERE id::text = $1`, sourceID, why)
	return err
}

// DeleteSource removes a document and its passages. An answer that cited it keeps its
// own copy of what it quoted, so the record of what was said stays readable.
func DeleteSource(ctx context.Context, tx pgx.Tx, id string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM notebook_sources WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- chunks --------------------------------------------------------------------------

// CurrentModel returns the model this organisation's passages were embedded with, or ""
// when none have been. The caller needs it to ask for the stale ones, and only a call to
// the embedding service can say what the model is today — so a change of model is noticed
// by the first batch that comes back under a different name, not predicted.
func CurrentModel(ctx context.Context, tx pgx.Tx) (string, error) {
	var model string
	err := tx.QueryRow(ctx, `SELECT embed_model FROM notebook_chunks WHERE embed_model IS NOT NULL LIMIT 1`).Scan(&model)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return model, err
}

// Unembedded returns passages with no vector, or one from another model. tx must be
// scoped to the org.
func Unembedded(ctx context.Context, tx pgx.Tx, model string, limit int) ([]Passage, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, source_id::text, seq, text FROM notebook_chunks
		WHERE vector IS NULL OR embed_model IS DISTINCT FROM $1 ORDER BY id LIMIT $2`, model, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Passage, error) {
		var p Passage
		err := r.Scan(&p.ID, &p.Source, &p.Seq, &p.Text)
		return p, err
	})
}

// SaveVector stores one passage's vector with the model that made it.
func SaveVector(ctx context.Context, tx pgx.Tx, id int64, model string, v []float32) error {
	_, err := tx.Exec(ctx, `UPDATE notebook_chunks SET vector = $2, embed_model = $3 WHERE id = $1`, id, v, model)
	return err
}

// ---- messages ------------------------------------------------------------------------

const messageCols = `id, notebook_id::text, asked_by, question, answer, status, citations, dropped, asked_at, answered_at`

func scanMessage(r pgx.Row) (Message, error) {
	var m Message
	var cites []byte
	if err := r.Scan(&m.ID, &m.Notebook, &m.AskedBy, &m.Question, &m.Answer, &m.Status, &cites, &m.Dropped, &m.AskedAt, &m.AnsweredAt); err != nil {
		return m, err
	}
	m.Citations = []Citation{}
	_ = json.Unmarshal(cites, &m.Citations)
	return m, nil
}

// Ask records a question. tx must be scoped to the org.
func Ask(ctx context.Context, tx pgx.Tx, org, notebookID, by, question string) (Message, error) {
	question = strings.Join(strings.Fields(question), " ")
	switch {
	case question == "":
		return Message{}, errors.New("notebook: the question is empty")
	case utf8.RuneCountInString(question) > MaxQuestion:
		return Message{}, errors.New("notebook: the question is too long")
	}
	if _, err := Load(ctx, tx, notebookID); err != nil {
		return Message{}, err
	}
	var waiting int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM notebook_messages WHERE notebook_id::text = $1 AND status = 'thinking'`, notebookID).Scan(&waiting); err != nil {
		return Message{}, err
	}
	if waiting >= MaxThinking {
		return Message{}, ErrAnswersWaiting
	}
	return scanMessage(tx.QueryRow(ctx, `
		INSERT INTO notebook_messages (org_id, notebook_id, asked_by, question) VALUES ($1, $2, $3, $4)
		RETURNING `+messageCols, org, notebookID, by, question))
}

// LoadMessage reads one question.
func LoadMessage(ctx context.Context, tx pgx.Tx, id int64) (Message, error) {
	m, err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageCols+` FROM notebook_messages WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// Messages lists a notebook's conversation, oldest first.
func Messages(ctx context.Context, tx pgx.Tx, notebookID string, limit int) ([]Message, error) {
	rows, err := tx.Query(ctx, `SELECT `+messageCols+` FROM notebook_messages WHERE notebook_id::text = $1 ORDER BY asked_at LIMIT $2`, notebookID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) { return scanMessage(r) })
}

// SaveAnswer stores what the reader will see, with the passages it used.
func SaveAnswer(ctx context.Context, tx pgx.Tx, id int64, answer, status string, cites []Citation, dropped int) error {
	if cites == nil {
		cites = []Citation{}
	}
	raw, err := json.Marshal(cites)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE notebook_messages SET answer = $2, status = $3, citations = $4, dropped = $5, answered_at = now()
		WHERE id = $1`, id, answer, status, raw, dropped)
	return err
}
