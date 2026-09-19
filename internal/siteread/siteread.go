// Package siteread answers the Site and Fixes pages from stored rows only. Safe for the
// api role: no crawler, no external clients.
package siteread

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Crawl is one crawl's outcome.
type Crawl struct {
	ID         int64           `json:"id"`
	Trigger    string          `json:"trigger"`
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	Pages      int             `json:"pages"`
	Summary    json.RawMessage `json:"summary"`
	Error      *string         `json:"error"`
}

// Summary is the Site page header.
type Summary struct {
	Latest      *Crawl         `json:"latest"`    // the newest crawl, running or not
	LastDone    *Crawl         `json:"last_done"` // the newest finished crawl
	OpenBySev   map[string]int `json:"open_by_severity"`
	FixesByStat map[string]int `json:"fixes_by_status"`
}

func scanCrawl(row pgx.Row) (*Crawl, error) {
	var c Crawl
	err := row.Scan(&c.ID, &c.Trigger, &c.Status, &c.StartedAt, &c.FinishedAt, &c.Pages, &c.Summary, &c.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

const crawlCols = `id, trigger, status, started_at, finished_at, pages, summary, error`

// LoadSummary reads the Site page header.
func LoadSummary(ctx context.Context, tx pgx.Tx) (Summary, error) {
	s := Summary{OpenBySev: map[string]int{}, FixesByStat: map[string]int{}}
	var err error
	if s.Latest, err = scanCrawl(tx.QueryRow(ctx, `SELECT `+crawlCols+` FROM crawls ORDER BY started_at DESC LIMIT 1`)); err != nil {
		return s, err
	}
	if s.LastDone, err = scanCrawl(tx.QueryRow(ctx, `SELECT `+crawlCols+` FROM crawls WHERE status = 'done' ORDER BY started_at DESC LIMIT 1`)); err != nil {
		return s, err
	}
	for _, q := range []struct {
		sql string
		out map[string]int
	}{
		{`SELECT severity, count(*)::int FROM audit_findings WHERE status = 'open' GROUP BY severity`, s.OpenBySev},
		{`SELECT status, count(*)::int FROM fixes GROUP BY status`, s.FixesByStat},
	} {
		rows, err := tx.Query(ctx, q.sql)
		if err != nil {
			return s, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				return s, err
			}
			q.out[k] = n
		}
		if err := rows.Err(); err != nil {
			return s, err
		}
	}
	return s, nil
}

// Finding is one audit finding.
type Finding struct {
	ID         int64           `json:"id"`
	Rule       string          `json:"rule"`
	Severity   string          `json:"severity"`
	URL        *string         `json:"url"`
	Message    string          `json:"message"`
	Detail     json.RawMessage `json:"detail"`
	Status     string          `json:"status"`
	FirstSeen  time.Time       `json:"first_seen"`
	LastSeen   time.Time       `json:"last_seen"`
	ResolvedAt *time.Time      `json:"resolved_at"`
	FixID      *int64          `json:"fix_id"`
}

// LoadFindings lists findings with status, most severe first.
func LoadFindings(ctx context.Context, tx pgx.Tx, status string, limit int) ([]Finding, error) {
	rows, err := tx.Query(ctx, `
		SELECT f.id, f.rule, f.severity, f.url, f.message, f.detail, f.status, f.first_seen, f.last_seen, f.resolved_at, x.id
		FROM audit_findings f LEFT JOIN fixes x ON x.org_id = f.org_id AND x.source = 'audit' AND x.fingerprint = f.fingerprint
		WHERE f.status = $1
		ORDER BY CASE f.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, f.rule, f.url
		LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Finding, error) {
		var f Finding
		return f, r.Scan(&f.ID, &f.Rule, &f.Severity, &f.URL, &f.Message, &f.Detail, &f.Status, &f.FirstSeen, &f.LastSeen, &f.ResolvedAt, &f.FixID)
	})
}

// Fix is one recommended change.
type Fix struct {
	ID           int64           `json:"id"`
	Source       string          `json:"source"`
	Title        string          `json:"title"`
	Instructions string          `json:"instructions"`
	Snippet      *string         `json:"snippet"`
	SnippetLang  *string         `json:"snippet_lang"`
	PageURL      *string         `json:"page_url"`
	Severity     *string         `json:"severity"`
	Status       string          `json:"status"`
	Route        string          `json:"route"`
	CreatedAt    time.Time       `json:"created_at"`
	SentAt       *time.Time      `json:"sent_at"`
	LiveAt       *time.Time      `json:"live_at"`
	MeasuredAt   *time.Time      `json:"measured_at"`
	Outcome      json.RawMessage `json:"outcome"`
}

// LoadFixes lists fixes, optionally by status ("" for all), most severe and newest first.
func LoadFixes(ctx context.Context, tx pgx.Tx, status string, limit int) ([]Fix, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, source, title, instructions, snippet, snippet_lang, page_url, severity, status, route,
		       created_at, sent_at, live_at, measured_at, coalesce(outcome, 'null'::jsonb)
		FROM fixes WHERE $1 = '' OR status = $1
		ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, created_at DESC
		LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Fix, error) {
		var f Fix
		return f, r.Scan(&f.ID, &f.Source, &f.Title, &f.Instructions, &f.Snippet, &f.SnippetLang, &f.PageURL, &f.Severity,
			&f.Status, &f.Route, &f.CreatedAt, &f.SentAt, &f.LiveAt, &f.MeasuredAt, &f.Outcome)
	})
}

// PageRow is one crawled page.
type PageRow struct {
	URL             string    `json:"url"`
	FinalURL        string    `json:"final_url"`
	Status          int       `json:"status"`
	Title           *string   `json:"title"`
	MetaDescription *string   `json:"meta_description"`
	H1              *string   `json:"h1"`
	Noindex         bool      `json:"noindex"`
	WordCount       int       `json:"word_count"`
	JSONLDTypes     []string  `json:"json_ld_types"`
	LastCrawledAt   time.Time `json:"last_crawled_at"`
	OpenFindings    int       `json:"open_findings"`
}

// LoadPages lists crawled pages, those with the most open findings first.
func LoadPages(ctx context.Context, tx pgx.Tx, limit int) ([]PageRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.url, p.final_url, p.status, p.title, p.meta_description, p.h1, p.noindex, p.word_count, p.json_ld_types, p.last_crawled_at,
		       (SELECT count(*) FROM audit_findings f WHERE f.org_id = p.org_id AND f.url = p.url AND f.status = 'open')::int AS open
		FROM pages p ORDER BY open DESC, p.url LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PageRow, error) {
		var p PageRow
		return p, r.Scan(&p.URL, &p.FinalURL, &p.Status, &p.Title, &p.MetaDescription, &p.H1, &p.Noindex, &p.WordCount, &p.JSONLDTypes, &p.LastCrawledAt, &p.OpenFindings)
	})
}
