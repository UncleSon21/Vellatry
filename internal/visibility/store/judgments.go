package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/visibility/engine"
	"github.com/UncleSon21/vellatry/internal/visibility/judge"
)

// AnswerForJudging is what the judge needs about one stored answer.
type AnswerForJudging struct {
	AnswerID  int64
	PromptID  string
	Question  string
	Engine    string
	Round     int
	Text      string
	Mentioned bool
	Judged    bool // a judgment by this judge already exists
}

// LoadAnswerForJudging loads one answer and whether judgeName already judged it.
func LoadAnswerForJudging(ctx context.Context, tx pgx.Tx, answerID int64, judgeName string) (AnswerForJudging, error) {
	a := AnswerForJudging{AnswerID: answerID}
	err := tx.QueryRow(ctx, `
		SELECT a.prompt_id::text, p.text, a.engine, a.round, a.text, s.brand_mentioned,
		       EXISTS (SELECT 1 FROM judgments j WHERE j.answer_id = a.id AND j.judge = $2)
		FROM answers a JOIN prompts p ON p.id = a.prompt_id JOIN answer_signals s ON s.answer_id = a.id
		WHERE a.id = $1`, answerID, judgeName).
		Scan(&a.PromptID, &a.Question, &a.Engine, &a.Round, &a.Text, &a.Mentioned, &a.Judged)
	return a, err
}

// JudgmentOutcome is what recording a judgment changed.
type JudgmentOutcome struct {
	New      bool
	Opened   []Change
	Resolved []Change
}

// RecordJudgment stores a verdict for one answer, adds its sentiment to the daily rollup,
// and syncs the cell's sentiment and differentiator blindspots by majority of the judged
// answers in the round.
func RecordJudgment(ctx context.Context, tx pgx.Tx, orgID string, a AnswerForJudging, judgeName string, r judge.Result, declaredDifferentiators int) (JudgmentOutcome, error) {
	var out JudgmentOutcome
	rows := judge.Rows(r)
	negative, _ := json.Marshal(judge.FramingNegative(r))
	diffGap, _ := json.Marshal(judge.DifferentiatorGap(r, declaredDifferentiators))
	rows = append(rows,
		judge.Row{Dimension: "framing_negative", Value: negative},
		judge.Row{Dimension: "differentiator_gap", Value: diffGap},
	)
	inserted := int64(0)
	for _, row := range rows {
		var value any
		if row.Value != nil {
			value = row.Value
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO judgments (org_id, answer_id, dimension, label, score, value, evidence, judge)
			VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8)
			ON CONFLICT (answer_id, dimension, judge) DO NOTHING`,
			orgID, a.AnswerID, row.Dimension, row.Label, row.Score, value, row.Evidence, judgeName)
		if err != nil {
			return out, err
		}
		inserted += tag.RowsAffected()
	}
	if inserted == 0 {
		return out, nil // recorded by an earlier attempt
	}
	out.New = true

	if _, err := tx.Exec(ctx, `
		UPDATE visibility_daily SET sentiment_sum = sentiment_sum + $3, sentiment_n = sentiment_n + 1
		WHERE org_id = $1 AND engine = $2
		  AND day = (SELECT (collected_at AT TIME ZONE 'UTC')::date FROM answers WHERE id = $4)`,
		orgID, a.Engine, judge.SentimentScore(r), a.AnswerID); err != nil {
		return out, err
	}

	var phase string
	var round int
	err := tx.QueryRow(ctx, `SELECT phase, round FROM cells WHERE prompt_id = $1 AND engine = $2`, a.PromptID, a.Engine).Scan(&phase, &round)
	if errors.Is(err, pgx.ErrNoRows) || round != a.Round {
		return out, nil // a check-now answer or an older round: rollups only
	}
	if err != nil {
		return out, err
	}
	settled := engine.Phase(phase) == engine.Settled
	for _, dim := range []struct{ dimension, kind string }{
		{"framing_negative", "sentiment"},
		{"differentiator_gap", "differentiator"},
	} {
		var judged, positive int
		if err := tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE j.value = 'true'::jsonb)
			FROM judgments j JOIN answers a ON a.id = j.answer_id JOIN answer_tasks k ON k.id = a.task_id
			WHERE a.prompt_id = $1 AND a.engine = $2 AND a.round = $3 AND j.dimension = $4 AND j.judge = $5
			  AND k.purpose <> 'check'`,
			a.PromptID, a.Engine, a.Round, dim.dimension, judgeName).Scan(&judged, &positive); err != nil {
			return out, err
		}
		finding := engine.None
		if judged >= 2 && 2*positive > judged {
			finding = engine.Provisional
			if settled {
				finding = engine.Confirmed
			}
		}
		ch, change, err := SyncBlindspot(ctx, tx, orgID, a.PromptID, a.Engine, dim.kind, finding, settled, a.AnswerID, "")
		if err != nil {
			return out, err
		}
		switch change {
		case Opened:
			out.Opened = append(out.Opened, ch)
		case Resolved:
			out.Resolved = append(out.Resolved, ch)
		}
	}
	return out, nil
}
