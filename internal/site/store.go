package site

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

// StartCrawl records a crawl as running.
func StartCrawl(ctx context.Context, tx pgx.Tx, org, trigger string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO crawls (org_id, trigger) VALUES ($1, $2) RETURNING id`, org, trigger).Scan(&id)
	return id, err
}

// FinishCrawl records the outcome of a crawl.
func FinishCrawl(ctx context.Context, tx pgx.Tx, id int64, pages int, summary any, crawlErr error) error {
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	status, errText := "done", (*string)(nil)
	if crawlErr != nil {
		status = "failed"
		s := crawlErr.Error()
		errText = &s
	}
	_, err = tx.Exec(ctx, `UPDATE crawls SET status = $2, finished_at = now(), pages = $3, summary = $4, error = $5 WHERE id = $1`,
		id, status, pages, raw, errText)
	return err
}

// SavePages upserts the latest state of each crawled page.
func SavePages(ctx context.Context, tx pgx.Tx, org string, crawlID int64, pages []Page) error {
	for _, p := range pages {
		var types []string
		for _, j := range p.JSONLD {
			types = append(types, j.Types...)
		}
		if types == nil {
			types = []string{}
		}
		canonical := ""
		if len(p.Canonical) > 0 {
			canonical = p.Canonical[0]
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO pages (org_id, url, final_url, status, content_type, title, meta_description, h1, canonical, noindex,
			                   word_count, json_ld_types, content_hash, last_crawl_id, last_crawled_at)
			VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), nullif($7, ''), nullif($8, ''), nullif($9, ''), $10, $11, $12, nullif($13, ''), $14, now())
			ON CONFLICT (org_id, url) DO UPDATE SET final_url = EXCLUDED.final_url, status = EXCLUDED.status,
			    content_type = EXCLUDED.content_type, title = EXCLUDED.title, meta_description = EXCLUDED.meta_description,
			    h1 = EXCLUDED.h1, canonical = EXCLUDED.canonical, noindex = EXCLUDED.noindex, word_count = EXCLUDED.word_count,
			    json_ld_types = EXCLUDED.json_ld_types, content_hash = EXCLUDED.content_hash,
			    last_crawl_id = EXCLUDED.last_crawl_id, last_crawled_at = now()`,
			org, p.URL, p.FinalURL, p.Status, p.ContentType, p.Title, p.MetaDescription, strings.Join(p.H1, " | "), canonical,
			p.Noindex, p.WordCount, types, p.ContentHash, crawlID); err != nil {
			return err
		}
	}
	return nil
}

// FindingChanges is what a crawl changed.
type FindingChanges struct {
	Opened   []Finding
	Resolved []string // fingerprints
}

// SyncFindings records this crawl's findings. A finding seen again stays open (a
// dismissed one stays dismissed); an open finding not seen again is resolved, but only
// when this crawl actually looked at its page, so a smaller crawl cannot "fix" pages it
// never fetched.
func SyncFindings(ctx context.Context, tx pgx.Tx, org string, crawlID int64, findings []Finding, crawled map[string]bool) (FindingChanges, error) {
	var ch FindingChanges
	current := map[string]bool{}
	for _, f := range findings {
		current[f.Fingerprint] = true
		detail, _ := json.Marshal(f.Detail)
		if f.Detail == nil {
			detail = []byte("{}")
		}
		var inserted bool
		var previous string
		err := tx.QueryRow(ctx, `
			WITH prev AS (SELECT status FROM audit_findings WHERE org_id = $1 AND fingerprint = $2)
			INSERT INTO audit_findings (org_id, fingerprint, rule, severity, url, message, detail, crawl_id)
			VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8)
			ON CONFLICT (org_id, fingerprint) DO UPDATE SET
			    severity = EXCLUDED.severity, message = EXCLUDED.message, detail = EXCLUDED.detail, crawl_id = EXCLUDED.crawl_id,
			    last_seen = now(),
			    status = CASE WHEN audit_findings.status = 'dismissed' THEN 'dismissed' ELSE 'open' END,
			    resolved_at = CASE WHEN audit_findings.status = 'dismissed' THEN audit_findings.resolved_at ELSE NULL END
			RETURNING (xmax = 0), coalesce((SELECT status FROM prev), '')`,
			org, f.Fingerprint, f.Rule, f.Severity, f.URL, f.Message, detail, crawlID).Scan(&inserted, &previous)
		if err != nil {
			return ch, err
		}
		if inserted || previous == "resolved" {
			ch.Opened = append(ch.Opened, f)
		}
	}

	rows, err := tx.Query(ctx, `SELECT fingerprint, coalesce(url, '') FROM audit_findings WHERE status = 'open'`)
	if err != nil {
		return ch, err
	}
	type open struct{ fp, url string }
	opens, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (open, error) {
		var o open
		return o, r.Scan(&o.fp, &o.url)
	})
	if err != nil {
		return ch, err
	}
	for _, o := range opens {
		if current[o.fp] || (o.url != "" && !crawled[o.url]) {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE audit_findings SET status = 'resolved', resolved_at = now() WHERE org_id = $1 AND fingerprint = $2`, org, o.fp); err != nil {
			return ch, err
		}
		ch.Resolved = append(ch.Resolved, o.fp)
	}
	return ch, nil
}

// ProposeFixes records a fix for each newly opened finding that has one. A fix that was
// live and whose finding came back is proposed again (a regression).
func ProposeFixes(ctx context.Context, tx pgx.Tx, org string, opened []Finding, b BrandFacts) (int, error) {
	n := 0
	for _, f := range opened {
		fix, ok := FixFor(f, b)
		if !ok {
			continue
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO fixes (org_id, source, fingerprint, title, instructions, snippet, snippet_lang, page_url, severity)
			VALUES ($1, 'audit', $2, $3, $4, nullif($5, ''), nullif($6, ''), nullif($7, ''), $8)
			ON CONFLICT (org_id, source, fingerprint) DO UPDATE SET
			    status = CASE WHEN fixes.status IN ('live', 'measured') THEN 'proposed' ELSE fixes.status END,
			    title = EXCLUDED.title, instructions = EXCLUDED.instructions, snippet = EXCLUDED.snippet`,
			org, f.Fingerprint, fix.Title, fix.Instructions, fix.Snippet, fix.SnippetLang, f.URL, f.Severity)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// MarkFixesLive marks fixes whose findings were resolved as live.
func MarkFixesLive(ctx context.Context, tx pgx.Tx, org string, resolved []string) (int, error) {
	if len(resolved) == 0 {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE fixes SET status = 'live', live_at = now()
		WHERE org_id = $1 AND source = 'audit' AND fingerprint = ANY($2) AND status IN ('proposed', 'sent')`, org, resolved)
	return int(tag.RowsAffected()), err
}
