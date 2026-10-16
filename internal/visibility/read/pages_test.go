package read

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/testdb"
)

// The Blindspots page: the grid of topics against engines, and the list under it.
func TestLoadGridAndBlindspots(t *testing.T) {
	pool := testdb.Pool(t)
	org, brandID := testdb.NewOrg(t, pool, "grid")
	engines := []string{"chatgpt", "gemini", "ai_overview"}

	var mattress, pillow, promptID string
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		// Two topics the team tracks, and one they ruled out.
		for _, tp := range []struct {
			name   string
			demand int
			status string
			into   *string
		}{{"mattress", 5000, "active", &mattress}, {"pillow", 900, "active", &pillow}, {"waterbed", 9000, "out_of_scope", nil}} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, demand_monthly, status) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
				org, brandID, tp.name, tp.demand, tp.status).Scan(&id); err != nil {
				return err
			}
			if tp.into != nil {
				*tp.into = id
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO prompts (org_id, brand_id, topic_id, text, source, status) VALUES ($1, $2, $3, 'best mattress australia', 'template', 'active') RETURNING id::text`,
			org, brandID, mattress).Scan(&promptID); err != nil {
			return err
		}
		// Measured on two of the three engines: one finished, one still screening.
		for _, c := range []struct {
			engine, phase              string
			answers, present, mentions int
		}{{"chatgpt", "settled", 5, 5, 1}, {"gemini", "screening", 2, 2, 2}} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO cells (org_id, prompt_id, engine, round, phase, answers, present, mentions)
				VALUES ($1, $2, $3, 1, $4, $5, $6, $7)`, org, promptID, c.engine, c.phase, c.answers, c.present, c.mentions); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO blindspots (org_id, prompt_id, engine, kind, status, confirmed, winning_competitor, first_seen, last_seen)
			VALUES ($1, $2, 'chatgpt', 'displacement', 'open', true, 'Ecosa', now(), now()),
			       ($1, $2, 'gemini', 'visibility', 'open', false, NULL, now(), now()),
			       ($1, $2, 'chatgpt', 'sentiment', 'resolved', true, NULL, now(), now())`, org, promptID)
		return err
	})

	var grid []GridRow
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		grid, err = LoadGrid(ctx, tx, engines)
		return err
	})

	// Highest demand first, and a topic the team ruled out is not on the page at all.
	if len(grid) != 2 || grid[0].Topic != "mattress" || grid[1].Topic != "pillow" {
		t.Fatalf("grid = %+v, want mattress then pillow and no retired topic", grid)
	}
	m := grid[0]
	if len(m.Cells) != 3 {
		t.Fatalf("mattress cells = %+v, want one per engine asked for", m.Cells)
	}
	if c := m.Cells["chatgpt"]; c.State != "confirmed" || c.Prompts != 1 || c.Settled != 1 || f(c.MentionRate) != 20 {
		t.Errorf("chatgpt cell = %+v, want a confirmed cell mentioning the brand in 1 of 5", c)
	}
	if c := m.Cells["gemini"]; c.State != "screening" || f(c.MentionRate) != 100 {
		t.Errorf("gemini cell = %+v, want it still screening", c)
	}
	// An engine nothing has been asked on is unexplored, not a zero.
	if c := m.Cells["ai_overview"]; c.State != "unexplored" || c.MentionRate != nil || c.Prompts != 0 {
		t.Errorf("ai_overview cell = %+v, want unexplored with no rate", c)
	}
	// Only the open blindspots are counted, and on the engine they were found on.
	if m.Cells["chatgpt"].Blindspots != 1 || m.Cells["gemini"].Blindspots != 1 {
		t.Errorf("blindspot counts = %+v; the resolved one should not be counted", m.Cells)
	}
	// A topic with no questions yet is a row of unexplored cells, not a missing row.
	if p := grid[1]; len(p.Cells) != 3 || p.Cells["chatgpt"].State != "unexplored" {
		t.Errorf("a topic with no questions = %+v", p)
	}

	var open []Blindspot
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		open, err = LoadBlindspots(ctx, tx, "open")
		return err
	})
	if len(open) != 2 {
		t.Fatalf("open blindspots = %d, want the two that are open", len(open))
	}
	// A confirmed displacement outranks a provisional visibility gap on the same topic.
	if open[0].Kind != "displacement" || !open[0].Confirmed || open[0].Priority.Score <= open[1].Priority.Score {
		t.Errorf("order = %s(%v) then %s(%v)", open[0].Kind, open[0].Priority.Score, open[1].Kind, open[1].Priority.Score)
	}
	if open[0].Competitor == nil || *open[0].Competitor != "Ecosa" || open[0].Topic == nil || *open[0].Topic != "mattress" {
		t.Errorf("the displacement = %+v, want it to name who won and on what topic", open[0])
	}
	if open[0].Demand == nil || *open[0].Demand != 5000 || open[0].Priority.Demand == 0 {
		t.Errorf("priority components = %+v", open[0].Priority)
	}
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		resolved, err := LoadBlindspots(ctx, tx, "resolved")
		if err != nil {
			return err
		}
		if len(resolved) != 1 || resolved[0].Kind != "sentiment" {
			t.Errorf("resolved = %+v", resolved)
		}
		return nil
	})

	other, _ := testdb.NewOrg(t, pool, "grid-other")
	tenant(t, pool, other, func(ctx context.Context, tx pgx.Tx) error {
		g, err := LoadGrid(ctx, tx, engines)
		if err != nil {
			return err
		}
		b, err := LoadBlindspots(ctx, tx, "open")
		if err != nil {
			return err
		}
		if len(g) != 0 || len(b) != 0 {
			t.Errorf("another organisation saw %d topics and %d blindspots", len(g), len(b))
		}
		return nil
	})
}

// The answers behind a question: what each engine said, newest first, with the signals
// detection found and the judge's reading where there is one.
func TestLoadAnswers(t *testing.T) {
	pool := testdb.Pool(t)
	org, brandID := testdb.NewOrg(t, pool, "answers")

	var promptID string
	var judged int64
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO prompts (org_id, brand_id, text, source, status) VALUES ($1, $2, 'best mattress', 'manual', 'active') RETURNING id::text`,
			org, brandID).Scan(&promptID); err != nil {
			return err
		}
		for i, a := range []struct {
			engine      string
			minutesAgo  int
			text        string
			mentioned   bool
			position    int
			competitors []string
		}{
			{"chatgpt", 30, "Koala and Ecosa both make good mattresses.", true, 1, []string{"Ecosa"}},
			{"gemini", 10, "Ecosa is the pick of the bunch.", false, 0, []string{"Ecosa"}},
		} {
			var taskID, id int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO answer_tasks (org_id, prompt_id, engine, purpose, round, status)
				VALUES ($1, $2, $3, 'screen', 1, 'done') RETURNING id`, org, promptID, a.engine).Scan(&taskID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO answers (org_id, prompt_id, task_id, engine, round, collected_at, present, text, sources, fan_out, model, method_version)
				VALUES ($1, $2, $3, $4, 1, now() - make_interval(mins => $5), true, $6, $7::jsonb, $8, 'm', 'v1')
				RETURNING id`, org, promptID, taskID, a.engine, a.minutesAgo, a.text,
				`[{"domain":"koala.com"}]`, []string{"mattress reviews"}).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO answer_signals (answer_id, org_id, brand_mentioned, brand_count, brand_position, brand_cited,
				    competitors_mentioned, cited_competitors, sources_count, visibility_gap, displacement_gap, detector_version)
				VALUES ($1, $2, $3, $4, $5, false, $6, '{}', 1, false, false, 'd1')`,
				id, org, a.mentioned, a.position, a.position, a.competitors); err != nil {
				return err
			}
			if i == 0 {
				judged = id
			}
		}
		// Only the older answer was judged, and only one of its dimensions is a score.
		_, err := tx.Exec(ctx, `
			INSERT INTO judgments (org_id, answer_id, dimension, score, value, judge)
			VALUES ($1, $2, 'sentiment', 7.5, NULL, 'j1'), ($1, $2, 'role', NULL, $3::jsonb, 'j1')`,
			org, judged, `"recommended"`)
		return err
	})

	var out []Answer
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = LoadAnswers(ctx, tx, promptID, 10)
		return err
	})
	if len(out) != 2 {
		t.Fatalf("answers = %d", len(out))
	}
	if out[0].Engine != "gemini" {
		t.Errorf("answers are not newest first: %s then %s", out[0].Engine, out[1].Engine)
	}
	if out[0].Mentioned || len(out[0].Competitors) != 1 || out[0].Competitors[0] != "Ecosa" {
		t.Errorf("the gemini answer's signals = %+v", out[0])
	}
	if out[0].Sentiment != nil || out[0].Role != nil {
		t.Errorf("an unjudged answer carries a judgement: %+v", out[0])
	}
	if f(out[1].Sentiment) != 7.5 || out[1].Role == nil || *out[1].Role != "recommended" {
		t.Errorf("the judged answer = sentiment %v role %v", out[1].Sentiment, out[1].Role)
	}
	if len(out[1].FanOut) != 1 || len(out[1].Sources) == 0 {
		t.Errorf("what the engine searched and cited is missing: %+v", out[1])
	}
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		one, err := LoadAnswers(ctx, tx, promptID, 1)
		if err != nil {
			return err
		}
		if len(one) != 1 || one[0].Engine != "gemini" {
			t.Errorf("limit 1 = %+v, want the newest alone", one)
		}
		return nil
	})
}

// The Sources page: who the engines cite when they answer about you.
func TestLoadSources(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "sources")

	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		for _, c := range []struct {
			day            string
			engine, domain string
			n              int
		}{
			{"2026-08-01", "chatgpt", "productreview.com.au", 5},
			{"2026-08-02", "chatgpt", "productreview.com.au", 3},
			{"2026-08-01", "gemini", "productreview.com.au", 2},
			{"2026-08-01", "chatgpt", "koala.com", 4},
			{"2026-08-01", "chatgpt", "ecosa.com.au", 4},
			{"2026-08-01", "chatgpt", "reddit.com", 1},
			{"2026-07-01", "chatgpt", "old.example", 99}, // outside the period
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO source_citations_daily (org_id, day, engine, domain, citations) VALUES ($1, $2::date, $3, $4, $5)`,
				org, c.day, c.engine, c.domain, c.n); err != nil {
				return err
			}
		}
		return nil
	})

	var rows []SourceRow
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = LoadSources(ctx, tx, day1, day2, "koala.com", []string{"ecosa.com.au"}, 10)
		return err
	})
	if len(rows) != 4 {
		t.Fatalf("sources = %+v; only the period's citations belong here", rows)
	}
	// Most cited first, summed across days and engines, and typed by who owns it.
	if rows[0].Domain != "productreview.com.au" || rows[0].Citations != 10 || rows[0].Type != Review {
		t.Errorf("top source = %+v, want 10 citations of a review site", rows[0])
	}
	if rows[0].ByEngine["chatgpt"] != 8 || rows[0].ByEngine["gemini"] != 2 {
		t.Errorf("by engine = %v", rows[0].ByEngine)
	}
	// Equal citations are broken by name, so the table does not reshuffle itself.
	if rows[1].Domain != "ecosa.com.au" || rows[1].Type != Competitor || rows[2].Domain != "koala.com" || rows[2].Type != Owned {
		t.Errorf("the tied pair = %+v and %+v, want them in name order and typed", rows[1], rows[2])
	}
	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		top, err := LoadSources(ctx, tx, day1, day2, "koala.com", nil, 2)
		if err != nil {
			return err
		}
		if len(top) != 2 {
			t.Errorf("limit 2 returned %d", len(top))
		}
		// With no competitors configured, their domain is just another site.
		all, err := LoadSources(ctx, tx, day1, day2, "koala.com", nil, 0)
		if err != nil {
			return err
		}
		for _, r := range all {
			if r.Domain == "ecosa.com.au" && r.Type == Competitor {
				t.Error("a domain was called a competitor's with no competitors tracked")
			}
		}
		if len(all) != 4 {
			t.Errorf("no limit returned %d rows, want all 4", len(all))
		}
		return nil
	})
}
