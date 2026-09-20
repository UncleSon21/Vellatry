package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/dataforseo"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/keywords"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/metering"
	"github.com/UncleSon21/vellatry/internal/warehouse"
)

// KeywordsAPI is what keyword research needs from DataForSEO.
type KeywordsAPI interface {
	KeywordIdeas(ctx context.Context, seeds []string, locationCode int, language string, limit int) ([]dataforseo.KeywordData, error)
	RankedKeywords(ctx context.Context, domain string, locationCode int, language string, limit int) ([]dataforseo.RankedKeyword, error)
	PostOrganic(ctx context.Context, reqs []dataforseo.OrganicRequest) ([]dataforseo.Posted, error)
	GetOrganic(ctx context.Context, taskID string) (dataforseo.OrganicSERP, bool, error)
}

// Keywords holds the keyword research jobs' dependencies.
type Keywords struct {
	Pool      *pgxpool.Pool
	Bus       *events.Bus
	Logger    *slog.Logger
	Notify    *Automation
	Research  KeywordsAPI         // nil: keyword research is off
	Warehouse warehouse.Warehouse // nil: topics map to pages without Search Console

	Ideas              int           // keyword ideas per run
	CompetitorKeywords int           // ranking keywords per competitor
	MaxSERPs           int           // search-result fetches per run: the cost of a run
	SERPFresh          time.Duration // how long stored results count as current
	CollectEvery       time.Duration // between checks for a queued result
	MaxCollect         time.Duration // how long to wait for one before giving up
	RefreshEvery       time.Duration // between automatic runs
	Now                func() time.Time
	// Enqueue adds a follow-up job. Tests replace it; in the worker it inserts through
	// the running job's own client.
	Enqueue func(ctx context.Context, args river.JobArgs) error
}

func (k *Keywords) defaults() {
	if k.Ideas <= 0 {
		k.Ideas = 500
	}
	if k.CompetitorKeywords <= 0 {
		k.CompetitorKeywords = 200
	}
	if k.MaxSERPs <= 0 {
		k.MaxSERPs = 400
	}
	if k.SERPFresh <= 0 {
		k.SERPFresh = 30 * 24 * time.Hour
	}
	if k.CollectEvery <= 0 {
		k.CollectEvery = time.Minute
	}
	if k.MaxCollect <= 0 {
		k.MaxCollect = 2 * time.Hour
	}
	if k.RefreshEvery <= 0 {
		k.RefreshEvery = 30 * 24 * time.Hour
	}
	if k.Now == nil {
		k.Now = time.Now
	}
	if k.Enqueue == nil {
		k.Enqueue = insertJob
	}
}

// Register adds the keyword research jobs.
func (k *Keywords) Register(ws *river.Workers) {
	k.defaults()
	river.AddWorker(ws, &keywordScheduleAllWorker{k: k})
	river.AddWorker(ws, &keywordRunWorker{k: k})
	river.AddWorker(ws, &keywordCollectWorker{k: k})
	river.AddWorker(ws, &keywordClusterWorker{k: k})
}

// PeriodicJobs checks daily which tenants are due a refresh.
func (k *Keywords) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(12*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return jobargs.KeywordScheduleAll{}, nil }, nil)}
}

type keywordScheduleAllWorker struct {
	river.WorkerDefaults[jobargs.KeywordScheduleAll]
	k *Keywords
}

func (w *keywordScheduleAllWorker) Work(ctx context.Context, _ *river.Job[jobargs.KeywordScheduleAll]) error {
	if w.k.Research == nil {
		return nil
	}
	orgs, err := orgsWithBrand(ctx, w.k.Pool)
	if err != nil {
		return err
	}
	var due []string
	for _, org := range orgs {
		var last *time.Time
		var inFlight bool
		if err := db.InTenant(ctx, w.k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			if inFlight, err = keywords.InFlight(ctx, tx); err != nil || inFlight {
				return err
			}
			last, err = keywords.LastRunAt(ctx, tx)
			return err
		}); err != nil {
			return err
		}
		if !inFlight && (last == nil || w.k.Now().Sub(*last) >= w.k.RefreshEvery) {
			due = append(due, org)
		}
	}
	return insertPerOrg(ctx, due, func(o string) river.JobArgs { return jobargs.KeywordRun{OrgID: o, Trigger: "schedule"} })
}

type keywordRunWorker struct {
	river.WorkerDefaults[jobargs.KeywordRun]
	k *Keywords
}

func (w *keywordRunWorker) Timeout(*river.Job[jobargs.KeywordRun]) time.Duration {
	return 10 * time.Minute
}

// runStats is what a run recorded, shown on the Research page.
type runStats struct {
	Seeds       int     `json:"seeds"`
	Ideas       int     `json:"ideas"`
	Competitor  int     `json:"competitor_keywords"`
	Keywords    int     `json:"keywords"`
	Rejected    int     `json:"rejected"`
	SERPsQueued int     `json:"serps_queued"`
	SERPsDone   int     `json:"serps_done"`
	Clusters    int     `json:"clusters"`
	TopicsNew   int     `json:"topics_new"`
	CostUSD     float64 `json:"cost_usd"`
}

func (w *keywordRunWorker) Work(ctx context.Context, job *river.Job[jobargs.KeywordRun]) error {
	k, org := w.k, job.Args.OrgID
	if k.Research == nil {
		return nil
	}
	trigger := job.Args.Trigger
	if trigger == "" {
		trigger = "manual"
	}

	var b brand.Brand
	var comps []brand.Competitor
	var seeds []string
	var scStats map[string]keywords.Seed
	var locationCode int
	var language string
	var runID int64
	err := db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		busy, err := keywords.InFlight(ctx, tx)
		if err != nil || busy {
			return errRunInFlight
		}
		if b, comps, err = brand.Load(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT location_code, language_code FROM org_settings`).Scan(&locationCode, &language); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			locationCode, language = 2036, "en"
		}
		since := k.Now().AddDate(0, -3, 0)
		scSeeds, err := keywords.SearchConsoleSeeds(ctx, tx, since, 50)
		if err != nil {
			return err
		}
		for _, s := range scSeeds {
			seeds = append(seeds, s.Keyword)
		}
		topics, err := keywords.TopicSeeds(ctx, tx, 30)
		if err != nil {
			return err
		}
		seeds = append(seeds, topics...)
		if scStats, err = keywords.SearchConsoleStats(ctx, tx, since); err != nil {
			return err
		}
		runID, err = keywords.StartRun(ctx, tx, org, trigger)
		return err
	})
	if errors.Is(err, errRunInFlight) || errors.Is(err, brand.ErrNoBrand) {
		return nil
	}
	if err != nil {
		return err
	}
	seeds = dedupe(append(append(seeds, job.Args.Seeds...), b.Name))
	if len(seeds) == 0 {
		return k.failRun(ctx, org, runID, errors.New("nothing to research yet: add a topic, or connect Search Console"))
	}

	stats := runStats{Seeds: len(seeds)}
	var upserts []keywords.Upsert
	var competitorNames []string
	for _, c := range comps {
		competitorNames = append(competitorNames, c.Name)
	}

	// Expansion: what else people search around these seeds.
	ideas, err := k.Research.KeywordIdeas(ctx, firstN(seeds, 25), locationCode, language, k.Ideas)
	if err != nil && !dataforseo.IsTransient(err) {
		return k.failRun(ctx, org, runID, err)
	}
	if err != nil {
		return err // transient: River retries the whole run
	}
	stats.Ideas = len(ideas)
	for _, i := range ideas {
		upserts = append(upserts, upsertOf(i, "idea", nil, competitorNames))
	}

	// The competitor gap: what they rank for.
	for _, c := range comps {
		for _, domain := range firstN(c.Domains, 1) {
			ranked, err := k.Research.RankedKeywords(ctx, domain, locationCode, language, k.CompetitorKeywords)
			if err != nil && !dataforseo.IsTransient(err) {
				k.Logger.WarnContext(ctx, "competitor keywords skipped", "org_id", org, "domain", domain, "error", err)
				continue
			}
			if err != nil {
				return err
			}
			stats.Competitor += len(ranked)
			for _, r := range ranked {
				if r.Rank > 20 {
					continue
				}
				host := strings.TrimPrefix(strings.ToLower(domain), "www.")
				upserts = append(upserts, upsertOf(r.KeywordData, "competitor", map[string]int{host: r.Rank}, competitorNames))
			}
		}
	}

	// The seeds themselves stay in the set: they are the searches we already appear for.
	for _, s := range seeds {
		upserts = append(upserts, keywords.Upsert{Keyword: s, Source: "seed"})
	}

	err = db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		n, err := keywords.Save(ctx, tx, org, runID, upserts)
		if err != nil {
			return err
		}
		stats.Keywords = n
		for _, u := range upserts {
			if u.Reject != "" {
				stats.Rejected++
			}
		}
		return keywords.ApplySearchConsole(ctx, tx, scStats)
	})
	if err != nil {
		return err
	}

	queued, cost, err := k.fetchSERPs(ctx, org, runID, locationCode, language)
	if err != nil {
		return k.failRun(ctx, org, runID, err)
	}
	stats.SERPsQueued, stats.CostUSD = queued, cost

	if err := db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := keywords.FinishRun(ctx, tx, runID, "collecting", stats, nil); err != nil {
			return err
		}
		_, err := k.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.KeywordRunStarted, SubjectID: itoa64(runID), Actor: "keywords",
			Payload: map[string]any{"run_id": runID, "keywords": stats.Keywords, "serps": queued}})
		return err
	}); err != nil {
		return err
	}
	if queued == 0 {
		// Nothing new to fetch: cluster what we already hold.
		return k.Enqueue(ctx, jobargs.KeywordCluster{OrgID: org, RunID: runID})
	}
	return nil
}

var errRunInFlight = errors.New("keyword research is already running")

func (k *Keywords) failRun(ctx context.Context, org string, runID int64, cause error) error {
	err := db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := keywords.FinishRun(ctx, tx, runID, "failed", runStats{}, cause); err != nil {
			return err
		}
		_, err := k.Notify.record(ctx, tx, org, automation.Notification{
			Kind: "keyword_run_failed", DedupeKey: "keyword_run:" + itoa64(runID), Severity: "warning",
			Title: "Keyword research could not finish", Body: cause.Error(), Link: "/topics/research", Delivery: "immediate",
		})
		return err
	})
	if err != nil {
		return err
	}
	k.Logger.WarnContext(ctx, "keyword run failed", "org_id", org, "run_id", runID, "error", cause)
	return nil
}

// fetchSERPs queues the search results the run needs, in batches of a hundred.
func (k *Keywords) fetchSERPs(ctx context.Context, org string, runID int64, locationCode int, language string) (int, float64, error) {
	var wanted []string
	if err := db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		wanted, err = keywords.NeedSERP(ctx, tx, k.Now().Add(-k.SERPFresh), k.MaxSERPs)
		return err
	}); err != nil {
		return 0, 0, err
	}
	queued, cost := 0, 0.0
	for start := 0; start < len(wanted); start += 100 {
		batch := wanted[start:min(start+100, len(wanted))]
		reqs := make([]dataforseo.OrganicRequest, len(batch))
		for i, kw := range batch {
			reqs[i] = dataforseo.OrganicRequest{Keyword: kw, LocationCode: locationCode, LanguageCode: language, Tag: itoa64(runID)}
		}
		posted, err := k.Research.PostOrganic(ctx, reqs)
		if err != nil {
			return queued, cost, err
		}
		var ids []int64
		if err := db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			for i, p := range posted {
				id, err := keywords.QueueSERP(ctx, tx, org, runID, batch[i], p.TaskID, p.Cost, p.Err)
				if err != nil {
					return err
				}
				cost += p.Cost
				if p.Err == nil && p.TaskID != "" && id > 0 {
					ids = append(ids, id)
				}
			}
			return metering.Record(ctx, tx, org, metering.Usage{Provider: "dataforseo", Purpose: "keywords.serp",
				Units: float64(len(posted)), CostUSD: cost, Ref: itoa64(runID)})
		}); err != nil {
			return queued, cost, err
		}
		for _, id := range ids {
			if err := k.Enqueue(ctx, jobargs.KeywordSERPCollect{OrgID: org, TaskID: id}); err != nil {
				return queued, cost, err
			}
		}
		queued += len(ids)
	}
	return queued, cost, nil
}

// insertJob enqueues one job from inside another job.
func insertJob(ctx context.Context, args river.JobArgs) error {
	_, err := river.ClientFromContext[pgx.Tx](ctx).Insert(ctx, args, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}})
	return err
}

type keywordCollectWorker struct {
	river.WorkerDefaults[jobargs.KeywordSERPCollect]
	k *Keywords
}

// Work waits for one keyword's search results, then stores them. It snoozes rather
// than polling in a loop, so a queued result costs nothing while it waits.
func (w *keywordCollectWorker) Work(ctx context.Context, job *river.Job[jobargs.KeywordSERPCollect]) error {
	k, org := w.k, job.Args.OrgID
	if k.Research == nil {
		return nil
	}
	var t keywords.SERPTask
	err := db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		t, err = keywords.OpenSERPTask(ctx, tx, job.Args.TaskID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already collected or given up on
	}
	if err != nil {
		return err
	}

	serp, ready, err := k.Research.GetOrganic(ctx, t.TaskID)
	switch {
	case err != nil && !dataforseo.IsTransient(err):
		return k.giveUp(ctx, org, t, err.Error())
	case err != nil:
		return err
	case !ready:
		if k.Now().Sub(t.CreatedAt) > k.MaxCollect {
			return k.giveUp(ctx, org, t, "the search results did not arrive in time")
		}
		return river.JobSnooze(k.CollectEvery)
	}
	return db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := keywords.SaveSERP(ctx, tx, t.ID, t.Keyword, serp.URLs, serp.Features, serp.Cost); err != nil {
			return err
		}
		return k.clusterWhenReady(ctx, tx, org, t.RunID)
	})
}

// giveUp closes one task that will never arrive and lets the run carry on without it.
func (k *Keywords) giveUp(ctx context.Context, org string, t keywords.SERPTask, reason string) error {
	return db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := keywords.FailSERP(ctx, tx, t.ID, reason); err != nil {
			return err
		}
		return k.clusterWhenReady(ctx, tx, org, t.RunID)
	})
}

// clusterWhenReady starts clustering once the last search result of a run is in. The
// job is unique by its arguments, so several tasks finishing at once enqueue it once.
func (k *Keywords) clusterWhenReady(ctx context.Context, tx pgx.Tx, org string, runID int64) error {
	queued, _, err := keywords.SERPProgress(ctx, tx, runID)
	if err != nil || queued > 0 {
		return err
	}
	return k.Enqueue(ctx, jobargs.KeywordCluster{OrgID: org, RunID: runID})
}

type keywordClusterWorker struct {
	river.WorkerDefaults[jobargs.KeywordCluster]
	k *Keywords
}

func (w *keywordClusterWorker) Timeout(*river.Job[jobargs.KeywordCluster]) time.Duration {
	return 10 * time.Minute
}

// Work turns the collected search results into topics: clusters, names, intent, the
// page that should own each one, the opportunity score and what is wrong with it.
func (w *keywordClusterWorker) Work(ctx context.Context, job *river.Job[jobargs.KeywordCluster]) error {
	k, org, runID := w.k, job.Args.OrgID, job.Args.RunID
	var ks []keywords.Keyword
	var pages []keywords.CrawledPage
	var b brand.Brand
	var brandID string
	var stats runStats
	var scStats map[string]keywords.Seed
	err := db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if b, _, err = brand.Load(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM brands ORDER BY created_at LIMIT 1`).Scan(&brandID); err != nil {
			return err
		}
		if ks, err = keywords.ForClustering(ctx, tx); err != nil {
			return err
		}
		if pages, err = keywords.CrawledPages(ctx, tx, 2000); err != nil {
			return err
		}
		if scStats, err = keywords.SearchConsoleStats(ctx, tx, k.Now().AddDate(0, -3, 0)); err != nil {
			return err
		}
		_, done, err := keywords.SERPProgress(ctx, tx, runID)
		stats.SERPsDone = done
		return err
	})
	if errors.Is(err, brand.ErrNoBrand) || errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(ks) == 0 {
		return k.failRun(ctx, org, runID, errors.New("no search results were collected"))
	}

	clusters := keywords.Group(ks, keywords.MinSharedURLs)
	stats.Clusters = len(clusters)

	sc := k.queryPages(ctx, org, ks)
	updates := make([]keywords.TopicUpdate, 0, len(clusters))
	for _, c := range clusters {
		var clicks float64
		var posSum, posN float64
		for _, kw := range c.Keywords {
			if s, ok := scStats[kw]; ok {
				clicks += float64(s.Clicks)
				if s.Position > 0 {
					posSum += s.Position * float64(s.Impressions)
					posN += float64(s.Impressions)
				}
			}
		}
		pos := 0.0
		if posN > 0 {
			pos = posSum / posN
		}
		m := keywords.MapPage(c, sc, b.Domain, pages)
		updates = append(updates, keywords.TopicUpdate{
			Cluster: c, Opportunity: keywords.Score(c, clicks/3, pos), // three months of Search Console, per month
			Mapping: m, Issues: keywords.Issues(c, m, sc),
		})
	}
	sort.SliceStable(updates, func(i, j int) bool { return updates[i].Opportunity.Score > updates[j].Opportunity.Score })

	var res keywords.SyncResult
	err = db.InTenant(ctx, k.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if res, err = keywords.SyncTopics(ctx, tx, org, brandID, runID, updates); err != nil {
			return err
		}
		stats.TopicsNew = res.Created
		if err := keywords.FinishRun(ctx, tx, runID, "done", stats, nil); err != nil {
			return err
		}
		if _, err := k.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.KeywordRunCompleted, SubjectID: itoa64(runID), Actor: "keywords",
			Payload: map[string]any{"run_id": runID, "clusters": len(clusters), "topics_new": res.Created, "topics_updated": res.Updated}}); err != nil {
			return err
		}
		if res.Created == 0 {
			return nil
		}
		_, err = k.Notify.record(ctx, tx, org, automation.Notification{
			Kind: "topics_proposed", DedupeKey: "topics_proposed:" + itoa64(runID),
			Title: fmt.Sprintf("%d new topics to review", res.Created),
			Body:  "Keyword research grouped your searches into topics. Approve the ones worth tracking.", Link: "/topics", Delivery: "immediate",
		})
		return err
	})
	return err
}

// queryPages asks the warehouse which pages Google already shows for these keywords.
// Without Search Console, page mapping falls back to the search results and the site.
func (k *Keywords) queryPages(ctx context.Context, org string, ks []keywords.Keyword) map[string][]keywords.PageStat {
	out := map[string][]keywords.PageStat{}
	if k.Warehouse == nil {
		return out
	}
	queries := make([]string, 0, len(ks))
	for _, x := range ks {
		if x.SCImpressions > 0 && len(queries) < 1000 {
			queries = append(queries, x.Keyword)
		}
	}
	if len(queries) == 0 {
		return out
	}
	to := k.Now().UTC()
	rows, err := k.Warehouse.QueryPages(ctx, org, queries, to.AddDate(0, -3, 0), to)
	if err != nil {
		k.Logger.WarnContext(ctx, "page mapping without Search Console", "org_id", org, "error", err)
		return out
	}
	for _, r := range rows {
		q := strings.ToLower(r.Query)
		out[q] = append(out[q], keywords.PageStat{Page: r.Page, Clicks: r.Clicks, Impressions: r.Impressions})
	}
	return out
}

func upsertOf(d dataforseo.KeywordData, source string, competitors map[string]int, competitorNames []string) keywords.Upsert {
	u := keywords.Upsert{
		Keyword: d.Keyword, Source: source, Volume: d.SearchVolume, CPC: d.CPC, Competition: d.Competition,
		Difficulty: d.Difficulty, Intent: d.Intent, Competitors: competitors,
	}
	for _, m := range d.Monthly {
		u.Monthly = append(u.Monthly, keywords.MonthVolume{Year: m.Year, Month: m.Month, Volume: m.Volume})
	}
	if reject, reason := keywords.Reject(d.Keyword, competitorNames); reject {
		u.Reject = reason
	}
	return u
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func firstN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}
