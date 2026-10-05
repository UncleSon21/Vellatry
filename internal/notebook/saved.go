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

// What a session leaves behind: what you found, and what you asked to find it.

// MaxNote is as long as a note may be. A note is a finding, not a document; one longer
// than this is a document and belongs in the sources.
const MaxNote = 20_000

// MaxName is as long as a note's or recipe's name may be.
const MaxName = 160

// Note is something kept out of a notebook.
type Note struct {
	ID          int64      `json:"id"`
	Notebook    string     `json:"notebook_id"`
	Kind        string     `json:"kind"` // answer | written
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Citations   []Citation `json:"citations"`
	FromMessage *int64     `json:"from_message"`
	SavedBy     string     `json:"saved_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Frozen reports whether a note's words are the notebook's rather than the team's. The
// body of one is never edited: it is the record of what was said and of the passages it
// was said from, and a record that can be rewritten proves nothing.
func (n Note) Frozen() bool { return n.Kind == "answer" }

// Recipe is a question worth asking again.
type Recipe struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Question  string     `json:"question"`
	SavedBy   string     `json:"saved_by"`
	Runs      int        `json:"runs"`
	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// ErrFrozen is returned when something tries to rewrite an answer a notebook gave.
var ErrFrozen = errors.New("notebook: a note kept from an answer cannot be rewritten")

// ErrNotAnswered is returned when a question that has no answer yet is saved as a note.
var ErrNotAnswered = errors.New("notebook: that question has not been answered yet")

// ---- notes -----------------------------------------------------------------------------

const noteCols = `id, notebook_id::text, kind, title, body, citations, from_message, saved_by, created_at, updated_at`

func scanNote(r pgx.Row) (Note, error) {
	var n Note
	var cites []byte
	if err := r.Scan(&n.ID, &n.Notebook, &n.Kind, &n.Title, &n.Body, &cites, &n.FromMessage, &n.SavedBy, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return n, err
	}
	n.Citations = []Citation{}
	_ = json.Unmarshal(cites, &n.Citations)
	return n, nil
}

// KeepAnswer saves what the notebook said, with the passages it said it from. The words
// and the citations are copied rather than pointed at: a source may be deleted or
// re-read tomorrow, and this has to stay readable as what was said today.
func KeepAnswer(ctx context.Context, tx pgx.Tx, org string, messageID int64, title, by string) (Note, error) {
	m, err := LoadMessage(ctx, tx, messageID)
	if err != nil {
		return Note{}, err
	}
	if m.Status != "answered" && m.Status != "unanswerable" {
		return Note{}, ErrNotAnswered
	}
	if title = clean(title); title == "" {
		title = shorten(m.Question, MaxName)
	}
	cites, err := json.Marshal(m.Citations)
	if err != nil {
		return Note{}, err
	}
	return scanNote(tx.QueryRow(ctx, `
		INSERT INTO notebook_notes (org_id, notebook_id, kind, title, body, citations, from_message, saved_by)
		VALUES ($1, $2, 'answer', $3, $4, $5, $6, $7) RETURNING `+noteCols,
		org, m.Notebook, title, m.Answer, cites, m.ID, by))
}

// WriteNote saves the team's own words.
func WriteNote(ctx context.Context, tx pgx.Tx, org, notebookID, title, body, by string) (Note, error) {
	if _, err := Load(ctx, tx, notebookID); err != nil {
		return Note{}, err
	}
	body = strings.TrimSpace(body)
	switch {
	case body == "":
		return Note{}, errors.New("notebook: the note is empty")
	case len(body) > MaxNote:
		return Note{}, errors.New("notebook: the note is too long; add it as a source instead")
	}
	if title = clean(title); title == "" {
		title = shorten(body, MaxName)
	}
	return scanNote(tx.QueryRow(ctx, `
		INSERT INTO notebook_notes (org_id, notebook_id, kind, title, body, saved_by)
		VALUES ($1, $2, 'written', $3, $4, $5) RETURNING `+noteCols, org, notebookID, title, body, by))
}

// Notes lists a notebook's notes, newest first.
func Notes(ctx context.Context, tx pgx.Tx, notebookID string, limit int) ([]Note, error) {
	rows, err := tx.Query(ctx, `SELECT `+noteCols+` FROM notebook_notes WHERE notebook_id::text = $1 ORDER BY created_at DESC LIMIT $2`, notebookID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Note, error) { return scanNote(r) })
}

// LoadNote reads one note.
func LoadNote(ctx context.Context, tx pgx.Tx, id int64) (Note, error) {
	n, err := scanNote(tx.QueryRow(ctx, `SELECT `+noteCols+` FROM notebook_notes WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// EditNote retitles a note, and rewrites one the team wrote themselves. Passing body ""
// leaves the body alone, which is how a kept answer is retitled.
func EditNote(ctx context.Context, tx pgx.Tx, id int64, title, body string) (Note, error) {
	n, err := LoadNote(ctx, tx, id)
	if err != nil {
		return n, err
	}
	if body = strings.TrimSpace(body); body != "" {
		if n.Frozen() {
			return n, ErrFrozen
		}
		if len(body) > MaxNote {
			return n, errors.New("notebook: the note is too long; add it as a source instead")
		}
		n.Body = body
	}
	if t := clean(title); t != "" {
		n.Title = t
	}
	return scanNote(tx.QueryRow(ctx, `
		UPDATE notebook_notes SET title = $2, body = $3, updated_at = now() WHERE id = $1 RETURNING `+noteCols,
		id, n.Title, n.Body))
}

// DeleteNote removes a note.
func DeleteNote(ctx context.Context, tx pgx.Tx, id int64) error {
	tag, err := tx.Exec(ctx, `DELETE FROM notebook_notes WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- recipes ---------------------------------------------------------------------------

const recipeCols = `id, name, question, saved_by, runs, last_run_at, created_at`

func scanRecipe(r pgx.Row) (Recipe, error) {
	var c Recipe
	err := r.Scan(&c.ID, &c.Name, &c.Question, &c.SavedBy, &c.Runs, &c.LastRunAt, &c.CreatedAt)
	return c, err
}

// SaveRecipe keeps a question to ask again. Saving the same question twice returns the
// recipe that already exists rather than a second copy of it.
func SaveRecipe(ctx context.Context, tx pgx.Tx, org, name, question, by string) (Recipe, error) {
	question = clean(question)
	switch {
	case question == "":
		return Recipe{}, errors.New("notebook: the question is empty")
	case utf8.RuneCountInString(question) > MaxQuestion:
		return Recipe{}, errors.New("notebook: the question is too long")
	}
	if name = clean(name); name == "" {
		name = shorten(question, MaxName)
	}
	return scanRecipe(tx.QueryRow(ctx, `
		INSERT INTO notebook_recipes (org_id, name, question, saved_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, question) DO UPDATE SET name = EXCLUDED.name
		RETURNING `+recipeCols, org, name, question, by))
}

// Recipes lists the organisation's saved questions, most used first: what a team asks
// again is what a team found useful, and nothing else here judges that.
func Recipes(ctx context.Context, tx pgx.Tx, limit int) ([]Recipe, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+recipeCols+` FROM notebook_recipes ORDER BY runs DESC, created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Recipe, error) { return scanRecipe(r) })
}

// LoadRecipe reads one recipe.
func LoadRecipe(ctx context.Context, tx pgx.Tx, id int64) (Recipe, error) {
	c, err := scanRecipe(tx.QueryRow(ctx, `SELECT `+recipeCols+` FROM notebook_recipes WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// RecipeRun records that a recipe was used.
func RecipeRun(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `UPDATE notebook_recipes SET runs = runs + 1, last_run_at = now() WHERE id = $1`, id)
	return err
}

// DeleteRecipe removes a recipe. The questions it asked stay in their notebooks: a
// recipe is a shortcut, and deleting a shortcut deletes no work.
func DeleteRecipe(ctx context.Context, tx pgx.Tx, id int64) error {
	tag, err := tx.Exec(ctx, `DELETE FROM notebook_recipes WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// shorten names something after its own first words, which is what a person would have
// called it anyway.
func shorten(s string, max int) string {
	s = clean(s)
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}
