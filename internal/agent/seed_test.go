package agent

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/platform/db"
)

func tenant(t *testing.T, pool *pgxpool.Pool, org string, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(context.Background(), pool, org, fn); err != nil {
		t.Fatal(err)
	}
}

// seedForAnalyses gives a tenant a little of everything the eleven analyses read, so
// each one has something to work from: two weeks of AI answers either side of a drop,
// a competitor who rose while the brand fell, blindspots, fixes, search and referral
// rollups, and a published report to measure "since last time" against.
//
// The numbers are small and round on purpose. A test that has to do arithmetic to know
// what it expects is a test nobody will trust when it fails.
func seedForAnalyses(t *testing.T, pool *pgxpool.Pool, org, brandID string) {
	t.Helper()
	// The week of 1 September, and the week before it.
	week := func(start time.Time, i int) time.Time { return start.AddDate(0, 0, i) }
	thisWeek := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	lastWeek := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	tenant(t, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE brands SET name = 'Koala', domain = 'koala.com' WHERE id = $1`, brandID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO competitors (org_id, brand_id, name, domains) VALUES ($1, $2, 'Ecosa', $3)`,
			org, brandID, []string{"ecosa.com.au"}); err != nil {
			return err
		}

		// AI answers, two a day over four days a week: 6 of 8 mentioned the brand last
		// week and 2 of 8 this week, so visibility fell from 75% to 25%.
		for i, mentioned := range []int{2, 2, 1, 1} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, position_sum, position_n, sentiment_sum, sentiment_n, method_version)
				VALUES ($1, $2, 'chatgpt', 2, 2, $3, 2, 2, 14, 2, 'vis-1')`, org, week(lastWeek, i), mentioned); err != nil {
				return err
			}
		}
		for i, mentioned := range []int{1, 1, 0, 0} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, position_sum, position_n, sentiment_sum, sentiment_n, method_version)
				VALUES ($1, $2, 'chatgpt', 2, 2, $3, 2, 2, 10, 2, 'vis-1')`, org, week(thisWeek, i), mentioned); err != nil {
				return err
			}
		}
		// Who was mentioned: the brand fell, the competitor rose.
		for _, e := range []struct {
			day               time.Time
			name              string
			isBrand           bool
			answers, mentions int
		}{
			{lastWeek, "Koala", true, 3, 6},
			{lastWeek, "Ecosa", false, 1, 2},
			{thisWeek, "Koala", true, 1, 2},
			{thisWeek, "Ecosa", false, 3, 6},
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO visibility_daily_entities (org_id, day, engine, entity, is_brand, answers, mentions)
				VALUES ($1, $2, 'chatgpt', $3, $4, $5, $6)`, org, e.day, e.name, e.isBrand, e.answers, e.mentions); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO source_citations_daily (org_id, day, engine, domain, citations)
			VALUES ($1, $2, 'chatgpt', 'productreview.com.au', 5), ($1, $2, 'chatgpt', 'ecosa.com.au', 3)`, org, thisWeek); err != nil {
			return err
		}

		// A tracked topic with a question, a measured cell and two blindspots.
		var topicID, promptID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO topics (org_id, brand_id, name, demand_monthly, status) VALUES ($1, $2, 'mattress', 5000, 'active') RETURNING id::text`,
			org, brandID).Scan(&topicID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO prompts (org_id, brand_id, topic_id, text, source, status) VALUES ($1, $2, $3, 'best mattress australia', 'template', 'active') RETURNING id::text`,
			org, brandID, topicID).Scan(&promptID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO cells (org_id, prompt_id, engine, round, phase, answers, present, mentions, displacements)
			VALUES ($1, $2, 'chatgpt', 1, 'settled', 8, 8, 2, 3)`, org, promptID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO blindspots (org_id, prompt_id, engine, kind, status, confirmed, winning_competitor, first_seen, last_seen)
			VALUES ($1, $2, 'chatgpt', 'displacement', 'open', true, 'Ecosa', $3, $4),
			       ($1, $2, 'chatgpt', 'sentiment', 'open', false, NULL, $3, $4)`, org, promptID, lastWeek, thisWeek); err != nil {
			return err
		}

		// Search: a month either side, with a branded search, a non-branded one that
		// fell, and one that stopped entirely.
		for _, q := range []struct {
			month               time.Time
			query               string
			clicks, impressions int64
		}{
			{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "koala mattress", 400, 4000},
			{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "best mattress australia", 300, 9000},
			{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "mattress in a box", 100, 2000},
			{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "koala mattress", 420, 4100},
			{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "best mattress australia", 120, 8800},
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO search_query_monthly (org_id, month, query, clicks, impressions, position_sum)
				VALUES ($1, $2, $3, $4, $5, $6)`, org, q.month, q.query, q.clicks, q.impressions, float64(q.impressions)*4); err != nil {
				return err
			}
		}
		// Daily totals, which is what the overview reads: 28 days of clicks either side
		// of the period, falling in the second half.
		for i := 0; i < 56; i++ {
			day := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
			clicks := int64(40)
			if day.After(time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)) {
				clicks = 25
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO search_daily (org_id, day, clicks, impressions, position_sum)
				VALUES ($1, $2, $3, $4, $5)`, org, day, clicks, clicks*20, float64(clicks*20)*4); err != nil {
				return err
			}
		}
		for _, p := range []struct {
			month  time.Time
			page   string
			clicks int64
		}{
			{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "https://koala.com/mattress", 500},
			{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "https://koala.com/mattress", 620},
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO search_page_monthly (org_id, month, page, clicks, impressions, position_sum)
				VALUES ($1, $2, $3, $4, 5000, 20000)`, org, p.month, p.page, p.clicks); err != nil {
				return err
			}
		}
		for i := 0; i < 6; i++ {
			if _, err := tx.Exec(ctx, `
				INSERT INTO analytics_daily (org_id, day, channel, sessions, users, key_events)
				VALUES ($1, $2, 'Organic Search', 200, 180, 4)`, org, week(thisWeek, i)); err != nil {
				return err
			}
			for _, r := range []struct {
				day      time.Time
				sessions int
			}{{week(lastWeek, i), 4}, {week(thisWeek, i), 12}} {
				if _, err := tx.Exec(ctx, `
					INSERT INTO ai_referral_daily (org_id, day, assistant, sessions, users, key_events)
					VALUES ($1, $2, 'chatgpt.com', $3, $3, 1)`, org, r.day, r.sessions); err != nil {
					return err
				}
			}
		}

		// An answer the judge read as negative, so "what is AI saying that is wrong"
		// has something to report rather than nothing to judge.
		var taskID, answerID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO answer_tasks (org_id, prompt_id, engine, purpose, round, status)
			VALUES ($1, $2, 'chatgpt', 'screen', 1, 'done') RETURNING id`, org, promptID).Scan(&taskID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO answers (org_id, prompt_id, task_id, engine, round, collected_at, present, text, sources, fan_out, model, method_version)
			VALUES ($1, $2, $3, 'chatgpt', 1, $4, true, 'Koala is the pricier option and reviewers say it sleeps warm.', '[]'::jsonb, '{}', 'm', 'v1')
			RETURNING id`, org, promptID, taskID, week(thisWeek, 1)).Scan(&answerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO answer_signals (answer_id, org_id, brand_mentioned, brand_count, brand_position, brand_cited,
			    competitors_mentioned, cited_competitors, sources_count, visibility_gap, displacement_gap, detector_version)
			VALUES ($1, $2, true, 1, 2, false, '{Ecosa}', '{}', 0, false, true, 'd1')`, answerID, org); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO judgments (org_id, answer_id, dimension, value, evidence, judge)
			VALUES ($1, $2, 'framing_negative', 'true'::jsonb, 'reviewers say it sleeps warm', 'j1')`, org, answerID); err != nil {
			return err
		}

		// A fix that went live, and an audit finding behind another.
		if _, err := tx.Exec(ctx, `
			INSERT INTO fixes (org_id, source, fingerprint, title, instructions, page_url, severity, status, live_at)
			VALUES ($1, 'audit', 'fp-1', 'Add a meta description', 'Write one of 150 characters.', 'https://koala.com/mattress', 'warning', 'live', $2),
			       ($1, 'blindspot', 'fp-2', 'Answer the firmness question', 'Add a section on firmness.', 'https://koala.com/mattress', 'critical', 'proposed', NULL)`,
			org, thisWeek); err != nil {
			return err
		}

		// A published report, so "what changed since the last one" has a last one.
		var seriesID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO report_series (org_id, name, period) VALUES ($1, 'Monthly performance', 'month') RETURNING id::text`, org).Scan(&seriesID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO reports (org_id, series_id, period_start, period_end, title, snapshot, status, version, published_at)
			VALUES ($1, $2, '2026-08-01', '2026-08-31', 'Monthly performance: August 2026', '{}'::jsonb, 'published', 1, $3)`,
			org, seriesID, lastWeek)
		return err
	})
}
