package workers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
