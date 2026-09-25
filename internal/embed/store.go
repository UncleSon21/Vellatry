package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/jackc/pgx/v5"
)

// What a vector can stand for. A query's subject id is the query text itself: search
// queries have no id of their own.
const (
	KindTopic  = "topic"
	KindQuery  = "query"
	KindPrompt = "prompt"
)

// Stored is one saved vector.
type Stored struct {
	SubjectID string
	Model     string
	TextHash  string
	Vector    Vector
}

// Hash identifies the text a vector was made from, so a later run can tell the text has
// changed and the vector is stale. Case and spacing do not change the meaning, and the
// model ignores them, so neither does this.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(text), " "))))
	return hex.EncodeToString(sum[:])
}

// Load returns every stored vector of one kind. tx must be scoped to the org.
func Load(ctx context.Context, tx pgx.Tx, kind string) ([]Stored, error) {
	rows, err := tx.Query(ctx, `SELECT subject_id, model, text_hash, vector FROM embeddings WHERE kind = $1`, kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Stored, error) {
		var s Stored
		err := r.Scan(&s.SubjectID, &s.Model, &s.TextHash, &s.Vector)
		return s, err
	})
}

// Save writes one vector, replacing whatever was there for that subject.
func Save(ctx context.Context, tx pgx.Tx, orgID, kind, subjectID, model, textHash string, v Vector) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO embeddings (org_id, kind, subject_id, model, text_hash, vector, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (org_id, kind, subject_id) DO UPDATE
		SET model = EXCLUDED.model, text_hash = EXCLUDED.text_hash, vector = EXCLUDED.vector, updated_at = now()`,
		orgID, kind, subjectID, model, textHash, []float32(v))
	return err
}

// Forget removes vectors whose subject is gone, so a deleted topic does not keep
// matching. tx must be scoped to the org.
func Forget(ctx context.Context, tx pgx.Tx, kind string, subjectIDs []string) error {
	if len(subjectIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `DELETE FROM embeddings WHERE kind = $1 AND subject_id = ANY($2)`, kind, subjectIDs)
	return err
}
