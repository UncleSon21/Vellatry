// Package store is the Visibility engine's write side. Every function runs inside a
// transaction scoped to one org (db.InTenant) and is safe to repeat: an answer or a
// judgment already recorded is detected and not counted twice.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/visibility/engine"
)

// Settings are the tenant's engine settings.
type Settings struct {
	DailyAnswerBudget int
	DiscoverShare     float64
	Engines           []string
	LocationCode      int
	LanguageCode      string
	JudgeEnabled      bool
}

// DefaultSettings apply until an org_settings row exists.
var DefaultSettings = Settings{
	DailyAnswerBudget: 60, DiscoverShare: 0.6,
	Engines:      []string{"chatgpt", "gemini", "ai_overview"},
	LocationCode: 2036, LanguageCode: "en", JudgeEnabled: true,
}

// LoadSettings returns the org's settings, or the defaults.
func LoadSettings(ctx context.Context, tx pgx.Tx) (Settings, error) {
	s := DefaultSettings
	err := tx.QueryRow(ctx,
		`SELECT daily_answer_budget, discover_share::float8, engines, location_code, language_code, judge_enabled
		 FROM org_settings LIMIT 1`).
		Scan(&s.DailyAnswerBudget, &s.DiscoverShare, &s.Engines, &s.LocationCode, &s.LanguageCode, &s.JudgeEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings, nil
	}
	return s, err
}

const todayUTC = `date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`

// LoadPlanInput gathers what the planner needs for the org.
func LoadPlanInput(ctx context.Context, tx pgx.Tx, s Settings) (engine.PlanInput, error) {
	in := engine.PlanInput{DiscoverShare: s.DiscoverShare, Engines: s.Engines}

	var used int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM answer_tasks WHERE created_at >= `+todayUTC).Scan(&used); err != nil {
		return in, err
	}
	in.Budget = max(s.DailyAnswerBudget-used, 0)

	rows, err := tx.Query(ctx, `
		SELECT c.prompt_id::text, c.engine, c.round, c.phase, c.answers,
		       (SELECT count(*) FROM answer_tasks t
		         WHERE t.prompt_id = c.prompt_id AND t.engine = c.engine AND t.round = c.round
		           AND t.status IN ('pending', 'queued') AND t.purpose <> 'check')
		FROM cells c JOIN prompts p ON p.id = c.prompt_id
		WHERE c.phase <> 'settled' AND p.status IN ('active', 'tracked') AND c.engine = ANY($1)`, s.Engines)
	if err != nil {
		return in, err
	}
	in.Open, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (engine.OpenCell, error) {
		var c engine.OpenCell
		var phase string
		err := r.Scan(&c.PromptID, &c.Engine, &c.Round, &phase, &c.Answers, &c.Pending)
		c.Phase = engine.Phase(phase)
		return c, err
	})
	if err != nil {
		return in, err
	}

	rows, err = tx.Query(ctx, `
		SELECT p.id::text, coalesce(max(c.round), 0) + 1
		FROM prompts p LEFT JOIN cells c ON c.prompt_id = p.id
		WHERE p.status = 'tracked'
		GROUP BY p.id
		HAVING count(c.prompt_id) = 0
		    OR (bool_and(c.phase = 'settled') AND max(c.round_started_at) < now() - interval '7 days')
		ORDER BY max(c.round_started_at) NULLS FIRST`)
	if err != nil {
		return in, err
	}
	in.Due, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (engine.Due, error) {
		var d engine.Due
		return d, r.Scan(&d.PromptID, &d.NextRound)
	})
	if err != nil {
		return in, err
	}

	rows, err = tx.Query(ctx, `
		SELECT p.id::text, coalesce(t.demand_monthly, 0), p.created_at
		FROM prompts p LEFT JOIN topics t ON t.id = p.topic_id
		WHERE p.status = 'candidate' AND (t.id IS NULL OR t.status = 'active')
		ORDER BY coalesce(t.demand_monthly, 0) DESC, p.created_at
		LIMIT 200`)
	if err != nil {
		return in, err
	}
	in.Candidates, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (engine.Candidate, error) {
		var c engine.Candidate
		return c, r.Scan(&c.PromptID, &c.TopicDemand, &c.CreatedAt)
	})
	return in, err
}

// Task is one requested answer.
type Task struct {
	ID             int64
	OrgID          string
	PromptID       string
	PromptText     string
	Engine         string
	Purpose        engine.Purpose
	Round          int
	ProviderTaskID string
	Status         string
	CreatedAt      time.Time
}

// CreateTasks records the planned answers as pending tasks and opens (or starts a new
// round of) the cells they belong to.
func CreateTasks(ctx context.Context, tx pgx.Tx, orgID string, specs []engine.TaskSpec) ([]Task, error) {
	out := make([]Task, 0, len(specs))
	for _, s := range specs {
		if s.Purpose != engine.PurposeCheck {
			phase := engine.Screening
			if s.Purpose == engine.PurposeTrack {
				phase = engine.Confirming // tracked refreshes go straight to full depth
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO cells (org_id, prompt_id, engine, round, phase)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (prompt_id, engine) DO UPDATE
				  SET round = EXCLUDED.round, phase = EXCLUDED.phase, answers = 0, present = 0, mentions = 0,
				      displacements = 0, band = NULL, round_started_at = now(), updated_at = now()
				  WHERE cells.round < EXCLUDED.round`,
				orgID, s.PromptID, s.Engine, s.Round, string(phase))
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE prompts SET status = 'active', updated_at = now() WHERE id = $1 AND status = 'candidate'`, s.PromptID); err != nil {
				return nil, err
			}
		}
		t := Task{OrgID: orgID, PromptID: s.PromptID, Engine: s.Engine, Purpose: s.Purpose, Round: s.Round, Status: "pending"}
		err := tx.QueryRow(ctx, `
			INSERT INTO answer_tasks (org_id, prompt_id, engine, purpose, round)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id, created_at, (SELECT text FROM prompts WHERE id = $2)`,
			orgID, s.PromptID, s.Engine, string(s.Purpose), s.Round).Scan(&t.ID, &t.CreatedAt, &t.PromptText)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// MarkQueued records the provider's task id and what posting it cost.
func MarkQueued(ctx context.Context, tx pgx.Tx, taskID int64, providerTaskID string, costUSD float64) error {
	_, err := tx.Exec(ctx,
		`UPDATE answer_tasks SET status = 'queued', provider_task_id = $2, cost_usd = $3 WHERE id = $1 AND status = 'pending'`,
		taskID, providerTaskID, costUSD)
	return err
}

// MarkFailed records a task that will never produce an answer.
func MarkFailed(ctx context.Context, tx pgx.Tx, taskID int64, reason string) error {
	_, err := tx.Exec(ctx,
		`UPDATE answer_tasks SET status = 'failed', error = $2, completed_at = now() WHERE id = $1 AND status IN ('pending', 'queued')`,
		taskID, reason)
	return err
}

// GetTask loads one task.
func GetTask(ctx context.Context, tx pgx.Tx, id int64) (Task, error) {
	t := Task{ID: id}
	var purpose string
	var provider *string
	err := tx.QueryRow(ctx, `
		SELECT t.org_id::text, t.prompt_id::text, p.text, t.engine, t.purpose, t.round, t.provider_task_id, t.status, t.created_at
		FROM answer_tasks t JOIN prompts p ON p.id = t.prompt_id WHERE t.id = $1`, id).
		Scan(&t.OrgID, &t.PromptID, &t.PromptText, &t.Engine, &purpose, &t.Round, &provider, &t.Status, &t.CreatedAt)
	t.Purpose = engine.Purpose(purpose)
	if provider != nil {
		t.ProviderTaskID = *provider
	}
	return t, err
}
