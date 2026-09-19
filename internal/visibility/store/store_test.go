package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/testdb"
	"github.com/UncleSon21/vellatry/internal/visibility/engine"
	"github.com/UncleSon21/vellatry/internal/visibility/judge"
	"github.com/UncleSon21/vellatry/internal/visibility/read"
	"github.com/UncleSon21/vellatry/internal/visibility/store"
)

func inTenant(t *testing.T, pool *pgxpool.Pool, org string, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(context.Background(), pool, org, fn); err != nil {
		t.Fatal(err)
	}
}

// TestEngineLifecycle walks one prompt through screening, confirmation and settling on
// three engines, then checks blindspots, rollups, idempotency, judging and isolation.
func TestEngineLifecycle(t *testing.T) {
	pool := testdb.Pool(t)
	org, brandID := testdb.NewOrg(t, pool, "Koala")
	other, _ := testdb.NewOrg(t, pool, "Other")

	var promptID string
	inTenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := brand.AddCompetitor(ctx, tx, org, brandID, brand.Competitor{Name: "Ecosa", Domains: []string{"ecosa.com.au"}}); err != nil {
			return err
		}
		var topicID string
		if err := tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, demand_monthly) VALUES ($1, $2, 'mattress', 5000) RETURNING id::text`, org, brandID).Scan(&topicID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO prompts (org_id, brand_id, topic_id, text, source) VALUES ($1, $2, $3, 'What is the best mattress in Australia?', 'template') RETURNING id::text`,
			org, brandID, topicID).Scan(&promptID)
	})

	// Plan 1: a new candidate is screened on every engine, twice.
	var tasks []store.Task
	inTenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		s, err := store.LoadSettings(ctx, tx)
		if err != nil {
			return err
		}
		in, err := store.LoadPlanInput(ctx, tx, s)
		if err != nil {
			return err
		}
		if in.Budget != 60 || len(in.Candidates) != 1 {
			t.Fatalf("plan input: budget %d, candidates %d", in.Budget, len(in.Candidates))
		}
		tasks, err = store.CreateTasks(ctx, tx, org, engine.Plan(in))
		return err
	})
	if len(tasks) != 6 {
		t.Fatalf("planned %d tasks, want 6", len(tasks))
	}
	byEngine := map[string][]store.Task{}
	for _, task := range tasks {
		byEngine[task.Engine] = append(byEngine[task.Engine], task)
	}

	record := func(task store.Task, obs store.Observed) store.Outcome {
		t.Helper()
		var out store.Outcome
		inTenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			b, comps, err := brand.Load(ctx, tx)
			if err != nil {
				return err
			}
			be, ce := brand.Entities(b, comps)
			out, err = store.RecordAnswer(ctx, tx, task, obs, be, ce)
			return err
		})
		return out
	}
	competitorWins := store.Observed{Present: true, Text: "Ecosa is the best mattress. Many people like Ecosa.",
		Sources: []store.Source{{URL: "https://www.ecosa.com.au/mattress"}}}
	brandWins := store.Observed{Present: true, Text: "Koala is great. The Koala mattress ships fast."}
	noAnswer := store.Observed{Present: false}

	// ChatGPT: 0 of 2 mentions escalates to confirmation with provisional blindspots.
	record(byEngine["chatgpt"][0], competitorWins)
	out := record(byEngine["chatgpt"][1], competitorWins)
	if out.Cell.Phase != engine.Confirming || len(out.Opened) != 2 {
		t.Fatalf("after screening: phase %s, opened %+v", out.Cell.Phase, out.Opened)
	}
	// Recording the same task again changes nothing.
	if again := record(byEngine["chatgpt"][1], competitorWins); again.New {
		t.Error("a repeated answer was recorded twice")
	}

	// Gemini: 2 of 2 mentions settles as visible after screening.
	record(byEngine["gemini"][0], brandWins)
	gem := record(byEngine["gemini"][1], brandWins)
	if gem.Cell.Phase != engine.Settled || gem.Band != "visible" || !gem.Mentioned {
		t.Errorf("gemini: %+v", gem)
	}
	// AI Overview: no AI answer at all settles as no_answer, not a blindspot.
	record(byEngine["ai_overview"][0], noAnswer)
	aio := record(byEngine["ai_overview"][1], noAnswer)
	if aio.Cell.Phase != engine.Settled || aio.Band != engine.NoAnswer || len(aio.Opened) != 0 {
		t.Errorf("ai overview: %+v", aio)
	}

	// Plan 2: only the confirming cell needs work, 3 more answers.
	var confirm []store.Task
	inTenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		s, _ := store.LoadSettings(ctx, tx)
		in, err := store.LoadPlanInput(ctx, tx, s)
		if err != nil {
			return err
		}
		confirm, err = store.CreateTasks(ctx, tx, org, engine.Plan(in))
		return err
	})
	if len(confirm) != 3 || confirm[0].Purpose != engine.PurposeConfirm || confirm[0].Engine != "chatgpt" {
		t.Fatalf("plan 2 = %+v", confirm)
	}
	record(confirm[0], competitorWins)
	record(confirm[1], competitorWins)
	last := competitorWins
	last.FanOut = []string{"best mattress for side sleepers australia", "koala vs ecosa mattress", "x"}
	settled := record(confirm[2], last)
	if settled.Cell.Phase != engine.Settled || settled.Band != "blindspot" {
		t.Fatalf("chatgpt settled: %+v", settled)
	}
	if len(settled.Opened) != 2 || !settled.Opened[0].Confirmed {
		t.Errorf("confirmation should upgrade both blindspots: %+v", settled.Opened)
	}
	if settled.Harvested != 2 {
		t.Errorf("harvested %d fan-out prompts, want 2", settled.Harvested)
	}

	// Read side.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	inTenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		p, err := read.LoadPerformance(ctx, tx, today, today)
		if err != nil {
			return err
		}
		// 9 answers, 7 with an AI answer, 2 mentioning the brand.
		if p.Overall.Answers != 9 || p.Overall.Present != 7 || p.Overall.Mentioned != 2 {
			t.Errorf("overall = %+v", p.Overall)
		}
		bs, err := read.LoadBlindspots(ctx, tx, "open")
		if err != nil {
			return err
		}
		kinds := map[string]read.Blindspot{}
		for _, b := range bs {
			kinds[b.Kind] = b
		}
		if len(bs) != 2 || !kinds["visibility"].Confirmed || kinds["displacement"].Competitor == nil || *kinds["displacement"].Competitor != "Ecosa" {
			t.Errorf("blindspots = %+v", bs)
		}
		grid, err := read.LoadGrid(ctx, tx, []string{"chatgpt", "gemini", "ai_overview"})
		if err != nil {
			return err
		}
		if len(grid) != 1 || grid[0].Cells["chatgpt"].Blindspots != 2 || grid[0].Cells["gemini"].State != "confirmed" {
			t.Errorf("grid = %+v", grid)
		}
		return nil
	})

	// Judging: two negative verdicts on Gemini's answers open a sentiment blindspot.
	negative := judge.Result{
		Tone: judge.Scaled{Label: 2}, ComparativeFraming: judge.Scaled{Label: 3}, Role: judge.Role{Value: "neutral_mention"},
		Hedging: judge.Scaled{Label: 3}, Specificity: judge.Scaled{Label: 3}, Consistency: judge.Scaled{Label: 3}, Directness: judge.Scaled{Label: 3},
		QuestionCoverage: judge.Scaled{Label: 3}, Depth: judge.Scaled{Label: 3}, EvidenceQuality: judge.Scaled{Label: 3}, Recency: judge.Scaled{Label: 3},
	}
	var gemAnswers []int64
	inTenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM answers WHERE prompt_id = $1 AND engine = 'gemini' ORDER BY id`, promptID)
		if err != nil {
			return err
		}
		gemAnswers, err = pgx.CollectRows(rows, pgx.RowTo[int64])
		return err
	})
	var opened []store.Change
	for i, id := range gemAnswers {
		inTenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			a, err := store.LoadAnswerForJudging(ctx, tx, id, "test-judge")
			if err != nil {
				return err
			}
			if !a.Mentioned || a.Judged {
				t.Fatalf("answer %d: %+v", id, a)
			}
			res, err := store.RecordJudgment(ctx, tx, org, a, "test-judge", negative, 0)
			if err != nil {
				return err
			}
			if again, _ := store.RecordJudgment(ctx, tx, org, a, "test-judge", negative, 0); again.New {
				t.Error("a judgment was recorded twice")
			}
			if i == 1 {
				opened = res.Opened
			}
			return nil
		})
	}
	if len(opened) != 1 || opened[0].Kind != "sentiment" || !opened[0].Confirmed {
		t.Errorf("sentiment blindspot = %+v", opened)
	}

	// Another tenant sees none of it.
	inTenant(t, pool, other, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM cells) + (SELECT count(*) FROM answers) + (SELECT count(*) FROM blindspots)
			+ (SELECT count(*) FROM judgments) + (SELECT count(*) FROM visibility_daily)`).Scan(&n)
		if n != 0 {
			t.Errorf("another tenant saw %d visibility rows", n)
		}
		return err
	})
}
