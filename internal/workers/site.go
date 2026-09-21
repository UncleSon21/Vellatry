package workers

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/site"
)

// Site holds the crawl and audit job's dependencies.
type Site struct {
	Pool    *pgxpool.Pool
	Crawler *site.Crawler
	Bus     *events.Bus
	Logger  *slog.Logger
	// Scheme and host override for tests; production crawls https://<brand domain>/.
	HomeFor func(domain string) string
}

// Register adds the site jobs.
func (s *Site) Register(ws *river.Workers) {
	if s.HomeFor == nil {
		s.HomeFor = func(domain string) string { return "https://" + domain + "/" }
	}
	river.AddWorker(ws, &siteCrawlAllWorker{s: s})
	river.AddWorker(ws, &siteCrawlWorker{s: s})
}

// PeriodicJobs crawls every tenant weekly.
func (s *Site) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(7*24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return jobargs.SiteCrawlAll{}, nil }, nil)}
}

type siteCrawlAllWorker struct {
	river.WorkerDefaults[jobargs.SiteCrawlAll]
	s *Site
}

func (w *siteCrawlAllWorker) Work(ctx context.Context, _ *river.Job[jobargs.SiteCrawlAll]) error {
	var orgs []string
	if err := db.InSystem(ctx, w.s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT org_id::text FROM brands`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil || len(orgs) == 0 {
		return err
	}
	params := make([]river.InsertManyParams, len(orgs))
	for i, o := range orgs {
		params[i] = river.InsertManyParams{Args: jobargs.SiteCrawl{OrgID: o, Trigger: "schedule"},
			InsertOpts: &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 6 * 24 * time.Hour}}}
	}
	_, err := river.ClientFromContext[pgx.Tx](ctx).InsertMany(ctx, params)
	return err
}

type siteCrawlWorker struct {
	river.WorkerDefaults[jobargs.SiteCrawl]
	s *Site
}

func (w *siteCrawlWorker) Timeout(*river.Job[jobargs.SiteCrawl]) time.Duration {
	return 30 * time.Minute
}

// Summary is stored on the crawl and shown on the Site page.
type Summary struct {
	Home        string           `json:"home"`
	RobotsFound bool             `json:"robots_found"`
	LLMSTxt     bool             `json:"llms_txt"`
	Sitemaps    []string         `json:"sitemaps"`
	AIAccess    []site.BotAccess `json:"ai_access"`
	Findings    map[string]int   `json:"findings"` // by severity, this crawl
	Opened      int              `json:"opened"`
	Resolved    int              `json:"resolved"`
	FixesLive   int              `json:"fixes_live"`
	Partial     bool             `json:"partial"` // stopped early; findings on unvisited pages were left as they were
}

func (w *siteCrawlWorker) Work(ctx context.Context, job *river.Job[jobargs.SiteCrawl]) error {
	s, org := w.s, job.Args.OrgID
	trigger := job.Args.Trigger
	if trigger == "" {
		trigger = "manual"
	}
	var b brand.Brand
	var crawlID int64
	err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if b, _, err = brand.Load(ctx, tx); err != nil {
			return err
		}
		var running bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crawls WHERE status = 'running' AND started_at > now() - interval '1 hour')`).Scan(&running); err != nil {
			return err
		}
		if running {
			return errCrawlRunning
		}
		crawlID, err = site.StartCrawl(ctx, tx, org, trigger)
		return err
	})
	if errors.Is(err, brand.ErrNoBrand) || errors.Is(err, errCrawlRunning) {
		return nil
	}
	if err != nil {
		return err
	}

	home := s.HomeFor(b.Domain)
	var lastReported atomic.Int64
	res, crawlErr := s.Crawler.Crawl(ctx, home, func(fetched int) {
		last := lastReported.Load()
		if int64(fetched)-last < 25 || !lastReported.CompareAndSwap(last, int64(fetched)) {
			return
		}
		_ = db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.SiteCrawlProgress, SubjectID: itoa64(crawlID), Actor: "crawler",
				Payload: map[string]int{"pages": fetched}})
			return err
		})
	})
	if crawlErr == nil && len(res.Pages) == 0 {
		crawlErr = site.ErrUnreachable
	}
	// A crawl that hit its time limit still saves what it fetched, so the result must be
	// written on a context that outlives the job's; otherwise the crawl stays "running"
	// and blocks the next one for an hour.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if crawlErr != nil && len(res.Pages) == 0 {
		return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			if err := site.FinishCrawl(ctx, tx, crawlID, 0, Summary{Home: home}, crawlErr); err != nil {
				return err
			}
			_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.SiteCrawlFailed, SubjectID: itoa64(crawlID), Actor: "crawler",
				Payload: map[string]string{"error": crawlErr.Error()}})
			return err
		})
	}

	findings := site.Audit(res.Facts(), res.Pages)
	crawled := map[string]bool{}
	var top []site.Page
	for _, p := range res.Pages {
		crawled[p.URL] = true
		if p.Status == 200 && !p.Noindex && len(top) < 20 {
			top = append(top, p)
		}
	}
	facts := site.BrandFacts{Name: b.Name, Domain: b.Domain, Pages: top}
	if len(b.Differentiators) > 0 {
		facts.Summary = b.Name + ": " + b.Differentiators[0] + "."
	}
	summary := Summary{Home: home, RobotsFound: res.RobotsFound, LLMSTxt: res.LLMSTxt, Sitemaps: res.Sitemaps,
		AIAccess: site.AIAccess(res.Robots), Findings: map[string]int{}, Partial: crawlErr != nil}
	for _, f := range findings {
		summary.Findings[f.Severity]++
	}

	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := site.SavePages(ctx, tx, org, crawlID, res.Pages); err != nil {
			return err
		}
		changes, err := site.SyncFindings(ctx, tx, org, crawlID, findings, crawled)
		if err != nil {
			return err
		}
		if _, err := site.ProposeFixes(ctx, tx, org, changes.Opened, facts); err != nil {
			return err
		}
		live, err := site.MarkFixesLive(ctx, tx, org, changes.Resolved)
		if err != nil {
			return err
		}
		summary.Opened, summary.Resolved, summary.FixesLive = len(changes.Opened), len(changes.Resolved), live
		if err := suggestFromSite(ctx, tx, s.Bus, b, res.Pages[0]); err != nil {
			return err
		}
		if err := site.FinishCrawl(ctx, tx, crawlID, len(res.Pages), summary, nil); err != nil {
			return err
		}
		if live > 0 {
			if _, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.FixesLive, Actor: "crawler",
				Payload: map[string]int{"fixes": live}}); err != nil {
				return err
			}
		}
		done := domainevents.SiteCrawlCompletedPayload{Pages: len(res.Pages), Opened: len(changes.Opened), Resolved: len(changes.Resolved), FixesLive: live}
		for _, f := range changes.Opened {
			if f.Severity == site.Critical {
				done.CriticalOpened = append(done.CriticalOpened, f.Fingerprint)
			}
		}
		_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.SiteCrawlCompleted, SubjectID: itoa64(crawlID), Actor: "crawler", Payload: done})
		return err
	})
}

var errCrawlRunning = errors.New("a crawl is already running")

// suggestFromSite pre-fills setup from what the home page says about itself, while the
// team is still in the setup wizard. Afterwards the setup is theirs and the weekly
// crawl leaves it alone. Aliases merge in as suggestions (never over a list a person
// edited) and topics arrive proposed, so nothing is measured until someone approves it.
func suggestFromSite(ctx context.Context, tx pgx.Tx, bus *events.Bus, b brand.Brand, home site.Page) error {
	var onboarding bool
	if err := tx.QueryRow(ctx, `SELECT onboarded_at IS NULL FROM org_settings`).Scan(&onboarding); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !onboarding {
		return nil
	}
	sug := site.Suggest(home, b.Name)
	var out domainevents.BrandSuggestedPayload
	if len(sug.Aliases) > 0 {
		merged := append(slices.Clone(b.Aliases), sug.Aliases...)
		nb, changed := brand.Apply(b, brand.Patch{Aliases: &merged}, brand.BySuggested)
		if len(changed) > 0 {
			if err := brand.Save(ctx, tx, nb); err != nil {
				return err
			}
			for _, a := range nb.Aliases {
				if !slices.ContainsFunc(b.Aliases, func(x string) bool { return strings.EqualFold(x, a) }) {
					out.Aliases = append(out.Aliases, a)
				}
			}
		}
	}
	for _, t := range sug.Topics {
		tag, err := tx.Exec(ctx,
			`INSERT INTO topics (org_id, brand_id, name, source, status) VALUES ($1, $2, $3, 'site', 'proposed')
			 ON CONFLICT (org_id, lower(name)) DO NOTHING`, b.OrgID, b.ID, t)
		if err != nil {
			return err
		}
		out.Topics += int(tag.RowsAffected())
	}
	if len(out.Aliases) == 0 && out.Topics == 0 {
		return nil
	}
	_, err := bus.Emit(ctx, tx, b.OrgID, events.Event{Kind: domainevents.BrandSuggested, SubjectID: b.ID, Actor: "crawler", Payload: out})
	return err
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
