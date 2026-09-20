package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Asked is one question and its answer.
type Asked struct {
	ID         int64           `json:"id"`
	Question   string          `json:"question"`
	Context    json.RawMessage `json:"context"`
	Intent     *string         `json:"intent"`
	Confidence *float64        `json:"confidence"`
	Analysis   *string         `json:"analysis"`
	Status     string          `json:"status"`
	Answer     string          `json:"answer"`
	Bundle     json.RawMessage `json:"bundle"`
	UsedModel  bool            `json:"used_model"`
	Problems   json.RawMessage `json:"problems"`
	TookMS     *int            `json:"took_ms"`
	AskedBy    *string         `json:"asked_by"`
	CreatedAt  time.Time       `json:"created_at"`
	AnsweredAt *time.Time      `json:"answered_at"`
}

// ErrNoQuestion is returned when a question id is unknown.
var ErrNoQuestion = errors.New("agent: question not found")

const askedCols = `id, question, context, intent, confidence, analysis, status, answer, bundle, used_model, problems, took_ms, asked_by, created_at, answered_at`

func scanAsked(r pgx.Row) (Asked, error) {
	var a Asked
	err := r.Scan(&a.ID, &a.Question, &a.Context, &a.Intent, &a.Confidence, &a.Analysis, &a.Status, &a.Answer,
		&a.Bundle, &a.UsedModel, &a.Problems, &a.TookMS, &a.AskedBy, &a.CreatedAt, &a.AnsweredAt)
	return a, err
}

// Record stores a question and whatever is known about it so far. status "thinking"
// means the worker is still on it.
func Record(ctx context.Context, tx pgx.Tx, org, askedBy, question string, qctx Context, route Route, status string) (int64, error) {
	rawCtx, err := json.Marshal(qctx)
	if err != nil {
		return 0, err
	}
	slots, err := json.Marshal(route.Slots)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO agent_questions (org_id, asked_by, question, context, intent, confidence, slots, status)
		VALUES ($1, nullif($2, ''), $3, $4, nullif($5, ''), $6, $7, $8) RETURNING id`,
		org, askedBy, question, rawCtx, route.Intent, route.Confidence, slots, status).Scan(&id)
	return id, err
}

// Answered stores the answer to a question.
func Answered(ctx context.Context, tx pgx.Tx, id int64, b Bundle, answer string, usedModel bool, problems []string, took time.Duration) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	probs, err := json.Marshal(nonNil(problems))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE agent_questions SET status = 'answered', analysis = nullif($2, ''), answer = $3, bundle = $4,
		    used_model = $5, problems = $6, took_ms = $7, answered_at = now() WHERE id = $1`,
		id, b.Analysis, answer, raw, usedModel, probs, int(took.Milliseconds()))
	return err
}

// Failed records that a question could not be answered.
func Failed(ctx context.Context, tx pgx.Tx, id int64, status, why string) error {
	_, err := tx.Exec(ctx, `UPDATE agent_questions SET status = $2, answer = $3, answered_at = now() WHERE id = $1`, id, status, why)
	return err
}

// Load reads one question.
func Load(ctx context.Context, tx pgx.Tx, id int64) (Asked, error) {
	a, err := scanAsked(tx.QueryRow(ctx, `SELECT `+askedCols+` FROM agent_questions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNoQuestion
	}
	return a, err
}

// Recent lists the latest questions, for the dashboard's history.
func Recent(ctx context.Context, tx pgx.Tx, limit int) ([]Asked, error) {
	rows, err := tx.Query(ctx, `SELECT `+askedCols+` FROM agent_questions ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Asked, error) { return scanAsked(r) })
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
