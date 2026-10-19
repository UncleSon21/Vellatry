// A tenant with a month of plausible data in it, for looking at the product with
// something on the screen: a demo, or checking a page renders what it should once there
// is anything to render. Day-one empty states are easy to see by signing up; everything
// after that needs data, and waiting a month for the engine to collect it is not a way
// to find a broken table.
//
//	DATABASE_URL=postgres://vellatry_app:vellatry@localhost:5433/vellatry \
//	  go run ./spikes/02-demo-data -email you@example.com
//
// It writes rollups and findings directly, which is what the workers would have written.
// Nothing here is a fixture any test depends on, and nothing invents a real company: the
// brand is Freshly and the competitors are Ecosa and Emma, as the design rules require of
// anything shown in a demo.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/platform/db"
)

func main() {
	email := flag.String("email", "", "the signed-in address whose organisation to fill (required)")
	days := flag.Int("days", 35, "how many days of history to write")
	flag.Parse()
	if *email == "" {
		log.Fatal("give -email: the address you sign in with locally")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("set DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	var org, brandID string
	if err := db.InSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT b.org_id::text, b.id::text FROM brands b
			JOIN memberships m ON m.org_id = b.org_id JOIN users u ON u.id = m.user_id
			WHERE lower(u.email) = lower($1) ORDER BY b.created_at LIMIT 1`, *email).Scan(&org, &brandID)
	}); err != nil {
		log.Fatalf("finding the organisation for %s: %v (sign in and finish setup first)", *email, err)
	}

	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return fill(ctx, tx, org, brandID, *days)
	}); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("filled %s with %d days of demo data\n", org, *days)
}

var engines = []string{"chatgpt", "gemini", "ai_overview"}

func fill(ctx context.Context, tx pgx.Tx, org, brandID string, days int) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	start := today.AddDate(0, 0, -days)

	if _, err := tx.Exec(ctx, `UPDATE brands SET name = 'Freshly', domain = 'freshly.example' WHERE id = $1`, brandID); err != nil {
		return err
	}
	for _, c := range []struct {
		name, domain string
	}{{"Ecosa", "ecosa.example"}, {"Emma", "emma.example"}} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO competitors (org_id, brand_id, name, domains) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, org, brandID, c.name, []string{c.domain}); err != nil {
			return err
		}
	}

	// Topics, and a question for each: what the engine would have been asked.
	topics := []struct {
		name   string
		demand int
	}{
		{"mattress in a box", 8100}, {"best mattress australia", 5400},
		{"mattress for back pain", 2900}, {"cooling mattress", 1600},
	}
	prompts := map[string]string{}
	for _, t := range topics {
		var topicID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO topics (org_id, brand_id, name, demand_monthly, status, source) VALUES ($1, $2, $3, $4, 'active', 'manual')
			ON CONFLICT (org_id, lower(name)) DO UPDATE SET demand_monthly = EXCLUDED.demand_monthly, status = 'active'
			RETURNING id::text`, org, brandID, t.name, t.demand).Scan(&topicID); err != nil {
			return err
		}
		var promptID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO prompts (org_id, brand_id, topic_id, text, source, status) VALUES ($1, $2, $3, $4, 'template', 'tracked')
			ON CONFLICT (org_id, lower(text)) DO UPDATE SET status = 'tracked' RETURNING id::text`,
			org, brandID, topicID, "what is the "+t.name).Scan(&promptID); err != nil {
			return err
		}
		prompts[t.name] = promptID
	}

	// A month of answers. Visibility climbs over the period, with one engine lagging, so
	// every chart has a shape rather than a flat line.
	for d := 0; d < days; d++ {
		day := start.AddDate(0, 0, d)
		progress := float64(d) / float64(days)
		for i, engine := range engines {
			answers := 10 + i
			rate := 0.25 + 0.3*progress // 25% climbing to 55%
			if engine == "gemini" {
				rate = 0.18 + 0.1*progress // the laggard
			}
			mentioned := int(math.Round(rate * float64(answers)))
			if _, err := tx.Exec(ctx, `
				INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, position_sum, position_n, sentiment_sum, sentiment_n, method_version)
				VALUES ($1, $2, $3, $4, $4, $5, $6, $5, $7, $5, 'vis-1')
				ON CONFLICT (org_id, day, engine) DO UPDATE SET answers = EXCLUDED.answers, present = EXCLUDED.present,
				    mentioned = EXCLUDED.mentioned, position_sum = EXCLUDED.position_sum, position_n = EXCLUDED.position_n,
				    sentiment_sum = EXCLUDED.sentiment_sum, sentiment_n = EXCLUDED.sentiment_n`,
				org, day, engine, answers, mentioned, mentioned*2, float64(mentioned)*7.4); err != nil {
				return err
			}
			for _, e := range []struct {
				name    string
				isBrand bool
				share   float64
			}{{"Freshly", true, rate}, {"Ecosa", false, 0.45}, {"Emma", false, 0.3}} {
				n := int(math.Round(e.share * float64(answers)))
				if n == 0 {
					continue
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO visibility_daily_entities (org_id, day, engine, entity, is_brand, answers, mentions)
					VALUES ($1, $2, $3, $4, $5, $6, $6)
					ON CONFLICT (org_id, day, engine, entity) DO UPDATE SET answers = EXCLUDED.answers, mentions = EXCLUDED.mentions`,
					org, day, engine, e.name, e.isBrand, n); err != nil {
					return err
				}
			}
		}
		for _, s := range []struct {
			domain string
			n      int
		}{{"productreview.com.au", 6}, {"reddit.com", 4}, {"ecosa.example", 3}, {"freshly.example", 2}, {"choice.com.au", 2}} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO source_citations_daily (org_id, day, engine, domain, citations) VALUES ($1, $2, 'chatgpt', $3, $4)
				ON CONFLICT (org_id, day, engine, domain) DO UPDATE SET citations = EXCLUDED.citations`,
				org, day, s.domain, s.n); err != nil {
				return err
			}
		}
		// Search Console and the assistants that send visits.
		clicks := 180 + d*4
		if _, err := tx.Exec(ctx, `
			INSERT INTO search_daily (org_id, day, clicks, impressions, position_sum) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (org_id, day) DO UPDATE SET clicks = EXCLUDED.clicks, impressions = EXCLUDED.impressions,
			    position_sum = EXCLUDED.position_sum`, org, day, clicks, clicks*22, float64(clicks*22)*7.8); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ai_referral_daily (org_id, day, assistant, sessions, users, key_events) VALUES ($1, $2, 'chatgpt.com', $3, $3, 1)
			ON CONFLICT (org_id, day, assistant) DO UPDATE SET sessions = EXCLUDED.sessions, users = EXCLUDED.users`,
			org, day, 3+d/4); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO analytics_daily (org_id, day, channel, sessions, users, key_events) VALUES ($1, $2, 'Organic Search', $3, $4, 6)
			ON CONFLICT (org_id, day, channel) DO UPDATE SET sessions = EXCLUDED.sessions`, org, day, clicks*3, clicks*2); err != nil {
			return err
		}
	}

	// This month's and last month's searches and pages, for the two tables that read
	// them by month.
	for _, m := range []time.Time{monthOf(today), monthOf(today.AddDate(0, -1, 0))} {
		for _, q := range []struct {
			query  string
			clicks int
		}{{"freshly mattress", 820}, {"mattress in a box", 460}, {"best mattress australia", 310}, {"cooling mattress", 120}} {
			n := q.clicks
			if !m.Equal(monthOf(today)) {
				n = int(float64(n) * 0.82)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO search_query_monthly (org_id, month, query, clicks, impressions, position_sum) VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (org_id, month, query) DO UPDATE SET clicks = EXCLUDED.clicks`,
				org, m, q.query, n, n*18, float64(n*18)*6.2); err != nil {
				return err
			}
		}
		for _, p := range []struct {
			page   string
			clicks int
		}{{"https://freshly.example/mattress", 900}, {"https://freshly.example/compare", 310}, {"https://freshly.example/blog/firmness", 180}} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO search_page_monthly (org_id, month, page, clicks, impressions, position_sum) VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (org_id, month, page) DO UPDATE SET clicks = EXCLUDED.clicks`,
				org, m, p.page, p.clicks, p.clicks*20, float64(p.clicks*20)*5.4); err != nil {
				return err
			}
		}
	}

	// Cells and blindspots: one question the brand is losing on each engine.
	for name, promptID := range prompts {
		for i, engine := range engines {
			phase, mentions := "settled", 4
			if i == 2 {
				phase, mentions = "confirming", 1
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO cells (org_id, prompt_id, engine, round, phase, answers, present, mentions, displacements, round_started_at)
				VALUES ($1, $2, $3, 1, $4, 5, 5, $5, 2, $6)
				ON CONFLICT (prompt_id, engine) DO UPDATE SET phase = EXCLUDED.phase, mentions = EXCLUDED.mentions`,
				org, promptID, engine, phase, mentions, start); err != nil {
				return err
			}
		}
		if name == "best mattress australia" || name == "cooling mattress" {
			kind, comp := "displacement", "Ecosa"
			if name == "cooling mattress" {
				kind, comp = "visibility", ""
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO blindspots (org_id, prompt_id, engine, kind, status, confirmed, winning_competitor, first_seen, last_seen)
				VALUES ($1, $2, 'chatgpt', $3, 'open', true, nullif($4, ''), $5, $6)
				ON CONFLICT (prompt_id, engine, kind) DO NOTHING`,
				org, promptID, kind, comp, start, today); err != nil {
				return err
			}
		}
	}

	// A crawl, two findings and the fixes written from them.
	var crawlID int64
	// The shape site.AIAccess writes: agent, product, purpose, blocked. Inventing a
	// shape here would demo data the product never produces.
	summary := `{"home":"https://freshly.example","llms_txt":false,"ai_access":[` +
		`{"agent":"OAI-SearchBot","product":"ChatGPT search","purpose":"search","blocked":true},` +
		`{"agent":"ChatGPT-User","product":"ChatGPT browsing","purpose":"search","blocked":false},` +
		`{"agent":"GPTBot","product":"OpenAI training","purpose":"training","blocked":true},` +
		`{"agent":"PerplexityBot","product":"Perplexity","purpose":"search","blocked":false},` +
		`{"agent":"Claude-SearchBot","product":"Claude search","purpose":"search","blocked":false}],` +
		`"findings":{"critical":1,"warning":1}}`
	if err := tx.QueryRow(ctx, `
		INSERT INTO crawls (org_id, trigger, status, pages, started_at, finished_at, summary)
		VALUES ($1, 'manual', 'done', 24, $2, $2, $3::jsonb) RETURNING id`, org, today.AddDate(0, 0, -2), summary).Scan(&crawlID); err != nil {
		return err
	}
	for _, f := range []struct {
		rule, severity, message, url string
	}{
		{"meta_description_missing", "warning", "This page has no meta description.", "https://freshly.example/compare"},
		{"ai_crawler_blocked", "critical", "robots.txt blocks OAI-SearchBot, so ChatGPT cannot read the site to answer questions about you.", "https://freshly.example/robots.txt"},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_findings (org_id, crawl_id, fingerprint, rule, severity, message, url, status, first_seen, last_seen)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'open', $8, $8)
			ON CONFLICT (org_id, fingerprint) DO NOTHING`,
			org, crawlID, f.rule+":"+f.url, f.rule, f.severity, f.message, f.url, today.AddDate(0, 0, -2)); err != nil {
			return err
		}
	}
	for _, fx := range []struct {
		title, instructions, url, severity, status string
	}{
		{"Let ChatGPT read the site", "Remove the OAI-SearchBot Disallow from robots.txt. Blocking GPTBot is a separate choice and keeps you out of training, not out of answers.", "https://freshly.example/robots.txt", "critical", "proposed"},
		{"Write a meta description", "Add one of about 150 characters describing the comparison.", "https://freshly.example/compare", "warning", "live"},
	} {
		var liveAt any
		if fx.status == "live" {
			liveAt = today.AddDate(0, 0, -1)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO fixes (org_id, source, fingerprint, title, instructions, page_url, severity, status, live_at)
			VALUES ($1, 'audit', $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
			org, "fix:"+fx.url, fx.title, fx.instructions, fx.url, fx.severity, fx.status, liveAt); err != nil {
			return err
		}
	}
	return nil
}

func monthOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
