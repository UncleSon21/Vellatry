package keywords

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Run is one pass of keyword research.
type Run struct {
	ID         int64           `json:"id"`
	Trigger    string          `json:"trigger"`
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	Stats      json.RawMessage `json:"stats"`
	Error      *string         `json:"error"`
}

const runCols = `id, trigger, status, started_at, finished_at, stats, error`

// StartRun records a run as collecting.
func StartRun(ctx context.Context, tx pgx.Tx, org, trigger string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO keyword_runs (org_id, trigger) VALUES ($1, $2) RETURNING id`, org, trigger).Scan(&id)
	return id, err
}

// FinishRun records the outcome.
func FinishRun(ctx context.Context, tx pgx.Tx, id int64, status string, stats any, runErr error) error {
	raw, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	var errText *string
	if runErr != nil {
		s := runErr.Error()
		errText = &s
	}
	_, err = tx.Exec(ctx, `UPDATE keyword_runs SET status = $2, stats = $3, error = $4,
		finished_at = CASE WHEN $2 IN ('done', 'failed') THEN now() END WHERE id = $1`, id, status, raw, errText)
	return err
}

// InFlight reports whether a run is still working.
func InFlight(ctx context.Context, tx pgx.Tx) (bool, error) {
	var yes bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM keyword_runs WHERE status IN ('collecting', 'clustering')
		AND started_at > now() - interval '6 hours')`).Scan(&yes)
	return yes, err
}

// LastRunAt is when research last finished, for the monthly refresh.
func LastRunAt(ctx context.Context, tx pgx.Tx) (*time.Time, error) {
	var at *time.Time
	err := tx.QueryRow(ctx, `SELECT max(finished_at) FROM keyword_runs WHERE status = 'done'`).Scan(&at)
	return at, err
}

// ListRuns returns recent runs, newest first.
func ListRuns(ctx context.Context, tx pgx.Tx, limit int) ([]Run, error) {
	rows, err := tx.Query(ctx, `SELECT `+runCols+` FROM keyword_runs ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Run, error) {
		var x Run
		return x, r.Scan(&x.ID, &x.Trigger, &x.Status, &x.StartedAt, &x.FinishedAt, &x.Stats, &x.Error)
	})
}

// Seed is a keyword to expand from, with what Search Console already knows about it.
type Seed struct {
	Keyword     string
	Clicks      int64
	Impressions int64
	Position    float64
}

// SearchConsoleSeeds returns the searches the brand already appears for but does not
// win: positions 11 to 30, where a little work moves the most traffic.
func SearchConsoleSeeds(ctx context.Context, tx pgx.Tx, since time.Time, limit int) ([]Seed, error) {
	rows, err := tx.Query(ctx, `
		SELECT query, sum(clicks)::bigint, sum(impressions)::bigint, sum(position_sum) / nullif(sum(impressions), 0)
		FROM search_query_monthly WHERE month >= date_trunc('month', $1::date)
		GROUP BY query
		HAVING sum(impressions) > 0 AND sum(position_sum) / sum(impressions) BETWEEN 11 AND 30
		ORDER BY sum(impressions) DESC LIMIT $2`, since.Format(time.DateOnly), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Seed, error) {
		var s Seed
		return s, r.Scan(&s.Keyword, &s.Clicks, &s.Impressions, &s.Position)
	})
}

// SearchConsoleStats returns what Search Console holds for each keyword we track.
func SearchConsoleStats(ctx context.Context, tx pgx.Tx, since time.Time) (map[string]Seed, error) {
	rows, err := tx.Query(ctx, `
		SELECT query, sum(clicks)::bigint, sum(impressions)::bigint, sum(position_sum) / nullif(sum(impressions), 0)
		FROM search_query_monthly WHERE month >= date_trunc('month', $1::date) GROUP BY query`, since.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	out := map[string]Seed{}
	for rows.Next() {
		var s Seed
		var pos *float64
		if err := rows.Scan(&s.Keyword, &s.Clicks, &s.Impressions, &pos); err != nil {
			return nil, err
		}
		if pos != nil {
			s.Position = *pos
		}
		out[strings.ToLower(s.Keyword)] = s
	}
	return out, rows.Err()
}

// TopicSeeds returns the topics the team already cares about.
func TopicSeeds(ctx context.Context, tx pgx.Tx, limit int) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT name FROM topics WHERE status = 'active' ORDER BY demand_monthly DESC NULLS LAST LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Upsert is a keyword as a run found it.
type Upsert struct {
	Keyword     string
	Source      string
	Volume      int
	CPC         *float64
	Competition *float64
	Difficulty  *int
	Intent      string
	Monthly     []MonthVolume
	Competitors map[string]int // competitor domain -> position
	Reject      string         // non-empty: store it rejected, with the reason
}

// MonthVolume is one month of search volume.
type MonthVolume struct {
	Year   int `json:"year"`
	Month  int `json:"month"`
	Volume int `json:"volume"`
}

// Save upserts keywords. Demand is refreshed every run; a person's decision to approve
// or reject one is never overwritten.
func Save(ctx context.Context, tx pgx.Tx, org string, runID int64, ks []Upsert) (int, error) {
	n := 0
	for _, k := range ks {
		key := strings.ToLower(strings.TrimSpace(k.Keyword))
		if key == "" {
			continue
		}
		monthly, err := json.Marshal(nonNil(k.Monthly))
		if err != nil {
			return n, err
		}
		comps, err := json.Marshal(k.Competitors)
		if err != nil {
			return n, err
		}
		if k.Competitors == nil {
			comps = []byte("{}")
		}
		status, reason := "candidate", any(nil)
		if k.Reject != "" {
			status, reason = "rejected", k.Reject
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO keywords (org_id, keyword, source, status, reject_reason, search_volume, cpc, competition, difficulty,
			                      intent, monthly, competitors, run_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, ''), $11, $12, $13)
			ON CONFLICT (org_id, keyword) DO UPDATE SET
			    -- A keyword can arrive from several places in one run; a row that carries no
			    -- demand (a seed) must not erase the demand another source measured.
			    search_volume = CASE WHEN EXCLUDED.search_volume > 0 THEN EXCLUDED.search_volume ELSE keywords.search_volume END,
			    cpc = coalesce(EXCLUDED.cpc, keywords.cpc), competition = coalesce(EXCLUDED.competition, keywords.competition),
			    difficulty = coalesce(EXCLUDED.difficulty, keywords.difficulty),
			    intent = coalesce(EXCLUDED.intent, keywords.intent),
			    monthly = CASE WHEN EXCLUDED.search_volume > 0 THEN EXCLUDED.monthly ELSE keywords.monthly END,
			    competitors = keywords.competitors || EXCLUDED.competitors,
			    run_id = EXCLUDED.run_id, updated_at = now()`,
			org, key, k.Source, status, reason, k.Volume, k.CPC, k.Competition, k.Difficulty, k.Intent, monthly, comps, runID)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ApplySearchConsole copies what Search Console knows onto the keywords it covers, in
// one statement rather than one per keyword.
func ApplySearchConsole(ctx context.Context, tx pgx.Tx, stats map[string]Seed) error {
	if len(stats) == 0 {
		return nil
	}
	queries := make([]string, 0, len(stats))
	clicks := make([]int64, 0, len(stats))
	impressions := make([]int64, 0, len(stats))
	positions := make([]float64, 0, len(stats))
	for k, s := range stats {
		queries = append(queries, k)
		clicks = append(clicks, s.Clicks)
		impressions = append(impressions, s.Impressions)
		positions = append(positions, s.Position)
	}
	_, err := tx.Exec(ctx, `
		UPDATE keywords k SET sc_clicks = s.clicks, sc_impressions = s.impressions,
		    sc_position = nullif(s.position, 0), updated_at = now()
		FROM (SELECT unnest($1::text[]) AS query, unnest($2::bigint[]) AS clicks,
		             unnest($3::bigint[]) AS impressions, unnest($4::float8[]) AS position) s
		WHERE k.keyword = s.query`, queries, clicks, impressions, positions)
	return err
}

// NeedSERP returns the keywords worth fetching search results for: the ones with the
// most demand whose results we do not have, or have not refreshed lately.
func NeedSERP(ctx context.Context, tx pgx.Tx, staleBefore time.Time, limit int) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT keyword FROM keywords
		WHERE status <> 'rejected' AND (serp_fetched_at IS NULL OR serp_fetched_at < $1)
		ORDER BY search_volume DESC, keyword LIMIT $2`, staleBefore, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// QueueSERP records one queued task and returns its id (0 when it could not be queued).
func QueueSERP(ctx context.Context, tx pgx.Tx, org string, runID int64, keyword, taskID string, cost float64, taskErr error) (int64, error) {
	status, errText := "queued", any(nil)
	if taskErr != nil || taskID == "" {
		status = "failed"
		errText = "could not be queued"
		if taskErr != nil {
			errText = taskErr.Error()
		}
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO serp_tasks (org_id, run_id, keyword, provider_task_id, status, cost_usd, error)
		VALUES ($1, $2, $3, nullif($4, ''), $5, $6, $7)
		ON CONFLICT (org_id, provider_task_id) DO NOTHING
		RETURNING id`, org, runID, keyword, taskID, status, cost, errText).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil // the same provider task is already queued
	}
	if status == "failed" {
		return 0, err
	}
	return id, err
}

// SERPTask is a queued search-results task.
type SERPTask struct {
	ID        int64
	RunID     int64
	Keyword   string
	TaskID    string
	CreatedAt time.Time
}

// OpenSERPTask reads one queued task.
func OpenSERPTask(ctx context.Context, tx pgx.Tx, id int64) (SERPTask, error) {
	var t SERPTask
	err := tx.QueryRow(ctx, `SELECT id, run_id, keyword, coalesce(provider_task_id, ''), created_at
		FROM serp_tasks WHERE id = $1 AND status = 'queued'`, id).
		Scan(&t.ID, &t.RunID, &t.Keyword, &t.TaskID, &t.CreatedAt)
	return t, err
}

// SaveSERP stores one keyword's results and closes its task.
func SaveSERP(ctx context.Context, tx pgx.Tx, taskID int64, keyword string, urls, features []string, cost float64) error {
	if _, err := tx.Exec(ctx, `UPDATE keywords SET serp_urls = $2, serp_features = $3, serp_fetched_at = now(), updated_at = now()
		WHERE keyword = $1`, keyword, nonNil(urls), nonNil(features)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE serp_tasks SET status = 'done', completed_at = now(), cost_usd = $2 WHERE id = $1`, taskID, cost)
	return err
}

// FailSERP gives up on one task.
func FailSERP(ctx context.Context, tx pgx.Tx, taskID int64, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE serp_tasks SET status = 'failed', completed_at = now(), error = $2 WHERE id = $1 AND status = 'queued'`, taskID, reason)
	return err
}

// SERPProgress reports how many of a run's tasks are still queued and how many are done.
func SERPProgress(ctx context.Context, tx pgx.Tx, runID int64) (queued, done int, err error) {
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'queued'), count(*) FILTER (WHERE status = 'done') FROM serp_tasks WHERE run_id = $1`, runID).
		Scan(&queued, &done)
	return queued, done, err
}

// ForClustering loads the keywords that have search results to cluster on.
func ForClustering(ctx context.Context, tx pgx.Tx) ([]Keyword, error) {
	rows, err := tx.Query(ctx, `
		SELECT keyword, search_volume, difficulty, coalesce(intent, ''), serp_urls, serp_features,
		       sc_clicks, sc_impressions, coalesce(sc_position, 0)
		FROM keywords WHERE status <> 'rejected' AND cardinality(serp_urls) > 0`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Keyword, error) {
		var k Keyword
		return k, r.Scan(&k.Keyword, &k.Volume, &k.Difficulty, &k.Intent, &k.URLs, &k.Features, &k.SCClicks, &k.SCImpressions, &k.SCPosition)
	})
}

// CrawledPages loads the brand's own pages, for mapping a topic to one.
func CrawledPages(ctx context.Context, tx pgx.Tx, limit int) ([]CrawledPage, error) {
	rows, err := tx.Query(ctx, `SELECT url, coalesce(title, ''), coalesce(h1, ''), status, noindex FROM pages
		WHERE status = 200 AND NOT noindex ORDER BY url LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CrawledPage, error) {
		var p CrawledPage
		return p, r.Scan(&p.URL, &p.Title, &p.H1, &p.Status, &p.Noindex)
	})
}

// TopicUpdate is one clustered topic to store.
type TopicUpdate struct {
	Cluster     Cluster
	Opportunity Opportunity
	Mapping     Mapping
	Issues      []Issue
}

// SyncResult is what SyncTopics changed.
type SyncResult struct {
	Created  int `json:"created"`
	Updated  int `json:"updated"`
	Keywords int `json:"keywords"`
}

// SyncTopics stores the clusters as topics. A cluster that mostly matches an existing
// topic updates it (keeping its name and the team's decision about it); anything else
// becomes a proposed topic, which nobody measures until the team approves it.
func SyncTopics(ctx context.Context, tx pgx.Tx, org, brandID string, runID int64, updates []TopicUpdate) (SyncResult, error) {
	var res SyncResult
	existing, err := topicKeywords(ctx, tx)
	if err != nil {
		return res, err
	}
	used := map[string]bool{}
	for _, u := range updates {
		id, matched := bestTopic(u.Cluster.Keywords, existing, used)
		opp, err := json.Marshal(u.Opportunity)
		if err != nil {
			return res, err
		}
		issues, err := json.Marshal(nonNil(u.Issues))
		if err != nil {
			return res, err
		}
		demand := u.Cluster.Volume
		if matched {
			used[id] = true
			if _, err := tx.Exec(ctx, `
				UPDATE topics SET demand_monthly = $2, intent = nullif($3, ''), keyword_count = $4, page_url = nullif($5, ''),
				    page_source = nullif($6, ''), page_kind = nullif($7, ''), opportunity = $8, issues = $9, run_id = $10, updated_at = now()
				WHERE id::text = $1`,
				id, demand, u.Cluster.Intent, len(u.Cluster.Keywords), u.Mapping.URL, u.Mapping.Source, u.Mapping.Kind, opp, issues, runID); err != nil {
				return res, err
			}
			res.Updated++
		} else {
			var created bool
			err := tx.QueryRow(ctx, `
				INSERT INTO topics (org_id, brand_id, name, source, status, demand_monthly, intent, keyword_count, page_url,
				                    page_source, page_kind, opportunity, issues, run_id)
				VALUES ($1, $2, $3, 'keywords', 'proposed', $4, nullif($5, ''), $6, nullif($7, ''), nullif($8, ''), nullif($9, ''), $10, $11, $12)
				ON CONFLICT (org_id, lower(name)) DO UPDATE SET demand_monthly = EXCLUDED.demand_monthly, intent = EXCLUDED.intent,
				    keyword_count = EXCLUDED.keyword_count, page_url = EXCLUDED.page_url, page_source = EXCLUDED.page_source,
				    page_kind = EXCLUDED.page_kind, opportunity = EXCLUDED.opportunity, issues = EXCLUDED.issues,
				    run_id = EXCLUDED.run_id, updated_at = now()
				RETURNING id::text, (xmax = 0)`,
				org, brandID, u.Cluster.Name, demand, u.Cluster.Intent, len(u.Cluster.Keywords), u.Mapping.URL,
				u.Mapping.Source, u.Mapping.Kind, opp, issues, runID).Scan(&id, &created)
			if err != nil {
				return res, err
			}
			used[id] = true
			if created {
				res.Created++
			} else {
				res.Updated++
			}
		}
		for _, k := range u.Cluster.Keywords {
			if _, err := tx.Exec(ctx, `UPDATE keywords SET topic_id = $2::uuid, updated_at = now() WHERE keyword = $1`, k, id); err != nil {
				return res, err
			}
			res.Keywords++
		}
	}
	return res, nil
}

// topicKeywords maps each topic to the keywords already assigned to it.
func topicKeywords(ctx context.Context, tx pgx.Tx) (map[string]map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT topic_id::text, keyword FROM keywords WHERE topic_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for rows.Next() {
		var id, k string
		if err := rows.Scan(&id, &k); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]bool{}
		}
		out[id][k] = true
	}
	return out, rows.Err()
}

// SameTopicOverlap is how much of the smaller keyword set two clusters must share to
// count as the same topic across runs.
const SameTopicOverlap = 0.5

func bestTopic(keywords []string, existing map[string]map[string]bool, used map[string]bool) (string, bool) {
	type scored struct {
		id    string
		share float64
	}
	var best scored
	for id, set := range existing {
		if used[id] || len(set) == 0 {
			continue
		}
		hit := 0
		for _, k := range keywords {
			if set[k] {
				hit++
			}
		}
		if hit == 0 {
			continue
		}
		smaller := len(set)
		if len(keywords) < smaller {
			smaller = len(keywords)
		}
		share := float64(hit) / float64(smaller)
		if share > best.share || (share == best.share && id < best.id) {
			best = scored{id, share}
		}
	}
	if best.share >= SameTopicOverlap {
		return best.id, true
	}
	return "", false
}

// KeywordRow is a keyword as the dashboard lists it.
type KeywordRow struct {
	Keyword       string    `json:"keyword"`
	TopicID       *string   `json:"topic_id"`
	Topic         *string   `json:"topic"`
	Source        string    `json:"source"`
	Status        string    `json:"status"`
	RejectReason  *string   `json:"reject_reason"`
	SearchVolume  int       `json:"search_volume"`
	Difficulty    *int      `json:"difficulty"`
	Intent        *string   `json:"intent"`
	CPC           *float64  `json:"cpc"`
	SCClicks      int64     `json:"sc_clicks"`
	SCImpressions int64     `json:"sc_impressions"`
	SCPosition    *float64  `json:"sc_position"`
	Competitors   []string  `json:"competitors"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// List returns keywords for the dashboard, highest demand first.
func List(ctx context.Context, tx pgx.Tx, topicID, status, search string, limit int) ([]KeywordRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT k.keyword, k.topic_id::text, t.name, k.source, k.status, k.reject_reason, k.search_volume, k.difficulty, k.intent,
		       k.cpc, k.sc_clicks, k.sc_impressions, k.sc_position, k.competitors, k.updated_at
		FROM keywords k LEFT JOIN topics t ON t.id = k.topic_id
		WHERE ($1 = '' OR k.topic_id::text = $1) AND ($2 = '' OR k.status = $2) AND ($3 = '' OR k.keyword LIKE '%' || $3 || '%')
		ORDER BY k.search_volume DESC, k.keyword LIMIT $4`, topicID, status, strings.ToLower(search), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (KeywordRow, error) {
		var k KeywordRow
		var comps map[string]int
		if err := r.Scan(&k.Keyword, &k.TopicID, &k.Topic, &k.Source, &k.Status, &k.RejectReason, &k.SearchVolume, &k.Difficulty,
			&k.Intent, &k.CPC, &k.SCClicks, &k.SCImpressions, &k.SCPosition, &comps, &k.UpdatedAt); err != nil {
			return k, err
		}
		for d := range comps {
			k.Competitors = append(k.Competitors, d)
		}
		sort.Strings(k.Competitors)
		return k, nil
	})
}

// SetStatus records a person's decision about a keyword. A rejection is kept with its
// reason: it is the training data for the relevance classifier.
func SetStatus(ctx context.Context, tx pgx.Tx, keyword, status, reason string) error {
	tag, err := tx.Exec(ctx, `UPDATE keywords SET status = $2, reject_reason = nullif($3, ''), updated_at = now() WHERE keyword = $1`,
		strings.ToLower(keyword), status, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoKeyword
	}
	return nil
}

// ErrNoKeyword is returned when a keyword is not in the set.
var ErrNoKeyword = errors.New("keywords: not found")
