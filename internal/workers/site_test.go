package workers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/site"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

func TestSiteCrawlFindingsAndFixes(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "crawl")

	var blockSearchBot atomic.Bool
	blockSearchBot.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		if blockSearchBot.Load() {
			w.Write([]byte("User-agent: OAI-SearchBot\nDisallow: /\n"))
			return
		}
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html lang="en"><head><title>Crawl test brand: the homepage for this test</title>
			<meta name="viewport" content="width=device-width"></head><body><h1>Home</h1><a href="/about">About</a></body></html>`))
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>About</title></head><body><h1>About</h1></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := &Site{Pool: pool, Crawler: &site.Crawler{HTTP: srv.Client(), MaxPages: 20}, Logger: slog.Default(),
		Bus: events.NewBus(inserter, domainevents.Subscriptions()...), HomeFor: func(string) string { return srv.URL + "/" }}
	s.Register(river.NewWorkers())
	w := &siteCrawlWorker{s: s}

	if err := w.Work(ctx, job(jobargs.SiteCrawl{OrgID: org, Trigger: "manual"})); err != nil {
		t.Fatal(err)
	}
	var fixID int64
	err = db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var pages int
		var status string
		if err := tx.QueryRow(ctx, `SELECT status, pages FROM crawls ORDER BY id DESC LIMIT 1`).Scan(&status, &pages); err != nil {
			return err
		}
		if status != "done" || pages != 2 {
			t.Errorf("crawl = %s with %d pages", status, pages)
		}
		var sev string
		if err := tx.QueryRow(ctx, `SELECT severity FROM audit_findings WHERE rule = 'robots_blocks_search_bot' AND status = 'open'`).Scan(&sev); err != nil {
			return err
		}
		if sev != "critical" {
			t.Errorf("blocked search bot severity = %s", sev)
		}
		var snippet string
		if err := tx.QueryRow(ctx, `SELECT x.id, x.snippet FROM fixes x JOIN audit_findings f ON f.fingerprint = x.fingerprint
			WHERE f.rule = 'robots_blocks_search_bot'`).Scan(&fixID, &snippet); err != nil {
			return err
		}
		if snippet != "User-agent: OAI-SearchBot\nAllow: /" {
			t.Errorf("fix snippet = %q", snippet)
		}
		var llms int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM fixes WHERE title = 'Publish llms.txt' AND snippet LIKE '# crawl%'`).Scan(&llms); err != nil {
			return err
		}
		if llms != 1 {
			t.Errorf("llms.txt fixes = %d, want 1 built from the brand", llms)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The customer unblocks the bot; the next crawl resolves the finding and marks the fix live.
	blockSearchBot.Store(false)
	if err := w.Work(ctx, job(jobargs.SiteCrawl{OrgID: org, Trigger: "schedule"})); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var findingStatus, fixStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM audit_findings WHERE rule = 'robots_blocks_search_bot'`).Scan(&findingStatus); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status FROM fixes WHERE id = $1`, fixID).Scan(&fixStatus); err != nil {
			return err
		}
		if findingStatus != "resolved" || fixStatus != "live" {
			t.Errorf("after unblocking: finding %s, fix %s; want resolved and live", findingStatus, fixStatus)
		}
		var live int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1`, domainevents.FixesLive).Scan(&live)
		if live != 1 {
			t.Errorf("fix.live events = %d", live)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSiteCrawlSuggestsDuringOnboarding(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "koala")
	if err := db.InSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO org_settings (org_id) VALUES ($1)`, org)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	nav := `<nav><a href="/mattresses">Mattresses</a><a href="/sofas">Sofa Beds</a><a href="/about">About us</a></nav>`
	var extra atomic.Value
	extra.Store("")
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Koala</title><script type="application/ld+json">
			{"@type":"Organization","name":"koala","legalName":"Koala Sleep Pty Ltd"}</script></head><body>` +
			nav + extra.Load().(string) + `</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := &Site{Pool: pool, Crawler: &site.Crawler{HTTP: srv.Client(), MaxPages: 5}, Logger: slog.Default(),
		Bus: events.NewBus(inserter, domainevents.Subscriptions()...), HomeFor: func(string) string { return srv.URL + "/" }}
	s.Register(river.NewWorkers())
	w := &siteCrawlWorker{s: s}

	state := func() (aliases []string, proposed []string, suggested int) {
		t.Helper()
		err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT aliases FROM brands`).Scan(&aliases); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT name FROM topics WHERE status = 'proposed' AND source = 'site' ORDER BY name`)
			if err != nil {
				return err
			}
			if proposed, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1`, domainevents.BrandSuggested).Scan(&suggested)
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	if err := w.Work(ctx, job(jobargs.SiteCrawl{OrgID: org, Trigger: "onboarding"})); err != nil {
		t.Fatal(err)
	}
	aliases, proposed, suggested := state()
	if strings.Join(aliases, "|") != "Koala Sleep Pty Ltd" || strings.Join(proposed, "|") != "Mattresses|Sofa Beds" || suggested != 1 {
		t.Fatalf("after the first crawl: aliases %v, proposed %v, %d events", aliases, proposed, suggested)
	}

	// The team edits the aliases and declines a topic; a later crawl in the wizard adds
	// its new section but touches neither.
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE brands SET aliases = '{Koala Sleep}', provenance = '{"aliases":"user"}'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE topics SET status = 'out_of_scope' WHERE name = 'Sofa Beds'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	extra.Store(`<header><a href="/pillows">Pillows</a></header>`)
	if err := w.Work(ctx, job(jobargs.SiteCrawl{OrgID: org, Trigger: "manual"})); err != nil {
		t.Fatal(err)
	}
	aliases, proposed, suggested = state()
	if strings.Join(aliases, "|") != "Koala Sleep" || strings.Join(proposed, "|") != "Mattresses|Pillows" || suggested != 2 {
		t.Fatalf("after a person edited: aliases %v, proposed %v, %d events", aliases, proposed, suggested)
	}

	// Once setup is finished the weekly crawl proposes nothing.
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE org_settings SET onboarded_at = now()`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	extra.Store(`<header><a href="/bases">Bed Bases</a></header>`)
	if err := w.Work(ctx, job(jobargs.SiteCrawl{OrgID: org, Trigger: "schedule"})); err != nil {
		t.Fatal(err)
	}
	if _, proposed, suggested = state(); len(proposed) != 2 || suggested != 2 {
		t.Errorf("after onboarding: proposed %v, %d events", proposed, suggested)
	}
}
