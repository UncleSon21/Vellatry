package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"golang.org/x/oauth2"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/google"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/secrets"
	"github.com/UncleSon21/vellatry/internal/search"
	"github.com/UncleSon21/vellatry/internal/warehouse"
)

// GoogleAPI is what the sync jobs need from Google.
type GoogleAPI interface {
	Sites(ctx context.Context) ([]google.Site, error)
	Properties(ctx context.Context) ([]google.Property, error)
	DayRows(ctx context.Context, site string, day time.Time) ([]google.SearchRow, error)
	DayTotals(ctx context.Context, site string, from, to time.Time) ([]google.DayTotal, error)
	AnalyticsDay(ctx context.Context, property string, day time.Time) ([]google.AnalyticsRow, error)
}

// Search holds the Search Console and GA4 sync jobs' dependencies.
type Search struct {
	Pool      *pgxpool.Pool
	Box       *secrets.Box
	OAuth     *oauth2.Config
	Warehouse warehouse.Warehouse
	Bus       *events.Bus
	Logger    *slog.Logger

	// Replaceable in tests.
	NewClient func(ctx context.Context, refreshToken string) GoogleAPI
	Exchange  func(ctx context.Context, code string) (*oauth2.Token, error)
	Revoke    func(ctx context.Context, token string) error
	Now       func() time.Time

	BackfillDays int // first sync reaches back this far (Search Console keeps 16 months)
	RefreshDays  int // daily syncs re-pull this many recent days (data settles for 2-3)
	ChunkDays    int // days per range job
}

func (s *Search) defaults() {
	if s.NewClient == nil {
		s.NewClient = func(ctx context.Context, rt string) GoogleAPI { return google.NewClient(ctx, s.OAuth, rt) }
	}
	if s.Exchange == nil {
		s.Exchange = func(ctx context.Context, code string) (*oauth2.Token, error) { return s.OAuth.Exchange(ctx, code) }
	}
	if s.Revoke == nil {
		s.Revoke = func(ctx context.Context, token string) error {
			return google.Revoke(ctx, &http.Client{Timeout: 15 * time.Second}, token)
		}
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.BackfillDays == 0 {
		s.BackfillDays = 480
	}
	if s.RefreshDays == 0 {
		s.RefreshDays = 4
	}
	if s.ChunkDays == 0 {
		s.ChunkDays = 7
	}
}

// Register adds the sync jobs.
func (s *Search) Register(ws *river.Workers) {
	s.defaults()
	river.AddWorker(ws, &googleAuthorizeWorker{s: s})
	river.AddWorker(ws, &googleRevokeWorker{s: s})
	river.AddWorker(ws, &syncAllWorker{s: s})
	river.AddWorker(ws, &searchSyncOrgWorker{s: s})
	river.AddWorker(ws, &searchSyncRangeWorker{s: s})
	river.AddWorker(ws, &analyticsSyncOrgWorker{s: s})
	river.AddWorker(ws, &analyticsSyncRangeWorker{s: s})
}

// PeriodicJobs runs the daily sync.
func (s *Search) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return jobargs.SyncAll{}, nil }, nil)}
}

// ---- connections -------------------------------------------------------------------

type grant struct {
	Status string
	Secret []byte
	Config grantConfig
}

type grantConfig struct {
	Sites      []google.Site     `json:"sites"`
	Properties []google.Property `json:"properties"`
	Listed     bool              `json:"listed"`
}

func loadGrant(ctx context.Context, tx pgx.Tx) (grant, error) {
	var g grant
	var cfg []byte
	err := tx.QueryRow(ctx, `SELECT status, secret, config FROM connections WHERE kind = 'google'`).Scan(&g.Status, &g.Secret, &cfg)
	if err != nil {
		return g, err
	}
	_ = json.Unmarshal(cfg, &g.Config)
	return g, nil
}

// property returns the chosen property for kind, or "" when not connected.
func property(ctx context.Context, tx pgx.Tx, kind string) (string, error) {
	var status string
	var cfg []byte
	err := tx.QueryRow(ctx, `SELECT status, config FROM connections WHERE kind = $1`, kind).Scan(&status, &cfg)
	if errors.Is(err, pgx.ErrNoRows) || status != "connected" {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var c struct {
		Property string `json:"property"`
	}
	_ = json.Unmarshal(cfg, &c)
	return c.Property, nil
}

// markBroken records a connection that needs the customer to reconnect, and announces
// it: the dashboard shows it with a fix, never silently.
func (s *Search) markBroken(ctx context.Context, org, detail string) error {
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE connections SET status = 'broken', status_detail = $1, updated_at = now()
			WHERE kind IN ('google', 'search_console', 'ga4') AND status <> 'revoked'`, detail); err != nil {
			return err
		}
		_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.ConnectionBroken, Actor: "sync",
			Payload: domainevents.ConnectionPayload{Kind: "google", Detail: detail}})
		return err
	})
}

// client opens the stored grant for org.
func (s *Search) client(ctx context.Context, org string) (GoogleAPI, error) {
	var rt []byte
	err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		g, err := loadGrant(ctx, tx)
		if err != nil {
			return err
		}
		if g.Status != "connected" {
			return fmt.Errorf("%w: google grant is %s", google.ErrAuth, g.Status)
		}
		rt, err = s.Box.Open(org, g.Secret)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.NewClient(ctx, string(rt)), nil
}

const reconnect = "Google access stopped working. Reconnect Google to resume syncing."

type googleAuthorizeWorker struct {
	river.WorkerDefaults[jobargs.GoogleAuthorize]
	s *Search
}

// Work exchanges the one-time code (saving the refresh token before anything else can
// fail, so a retry never reuses a spent code), then lists the properties.
func (w *googleAuthorizeWorker) Work(ctx context.Context, job *river.Job[jobargs.GoogleAuthorize]) error {
	s, org := w.s, job.Args.OrgID
	var g grant
	if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		g, err = loadGrant(ctx, tx)
		return err
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	switch g.Status {
	case "pending":
		code, err := s.Box.Open(org, g.Secret)
		if err != nil {
			return s.markBroken(ctx, org, reconnect)
		}
		tok, err := s.Exchange(ctx, string(code))
		if err != nil || tok.RefreshToken == "" {
			s.Logger.WarnContext(ctx, "google code exchange failed", "org_id", org, "error", err)
			return s.markBroken(ctx, org, "Google sign-in expired or was cancelled. Connect Google again.")
		}
		sealed, err := s.Box.Seal(org, []byte(tok.RefreshToken))
		if err != nil {
			return err
		}
		if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE connections SET secret = $1, status = 'connected', status_detail = NULL, config = '{}', updated_at = now() WHERE kind = 'google'`, sealed)
			return err
		}); err != nil {
			return err
		}
	case "connected":
		if g.Config.Listed {
			return nil
		}
	default:
		return nil
	}

	c, err := s.client(ctx, org)
	if err != nil {
		return err
	}
	sites, err := c.Sites(ctx)
	if err == nil {
		var props []google.Property
		if props, err = c.Properties(ctx); err == nil {
			return s.saveListing(ctx, org, grantConfig{Sites: sites, Properties: props, Listed: true})
		}
	}
	if errors.Is(err, google.ErrAuth) {
		return s.markBroken(ctx, org, reconnect)
	}
	return err // transient: River retries; the refresh token is already saved
}

func (s *Search) saveListing(ctx context.Context, org string, cfg grantConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE connections SET config = $1, updated_at = now() WHERE kind = 'google'`, raw); err != nil {
			return err
		}
		_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.ConnectionPropertiesReady, Actor: "sync",
			Payload: map[string]int{"sites": len(cfg.Sites), "properties": len(cfg.Properties)}})
		return err
	})
}

type googleRevokeWorker struct {
	river.WorkerDefaults[jobargs.GoogleRevoke]
	s *Search
}

func (w *googleRevokeWorker) Work(ctx context.Context, job *river.Job[jobargs.GoogleRevoke]) error {
	s, org := w.s, job.Args.OrgID
	var secret []byte
	if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT secret FROM connections WHERE kind = 'google' AND status = 'revoked'`).Scan(&secret)
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if secret != nil {
		if rt, err := s.Box.Open(org, secret); err == nil {
			if err := s.Revoke(ctx, string(rt)); err != nil {
				s.Logger.WarnContext(ctx, "google revoke failed; wiping the token anyway", "org_id", org, "error", err)
			}
		}
	}
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE connections SET secret = NULL, config = '{}', updated_at = now() WHERE kind = 'google'`)
		return err
	})
}

// ---- scheduling --------------------------------------------------------------------

type syncAllWorker struct {
	river.WorkerDefaults[jobargs.SyncAll]
	s *Search
}

func (w *syncAllWorker) Work(ctx context.Context, _ *river.Job[jobargs.SyncAll]) error {
	type row struct{ org, kind string }
	var rows []row
	if err := db.InSystem(ctx, w.s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT org_id::text, kind FROM connections WHERE kind IN ('search_console', 'ga4') AND status = 'connected'`)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (row, error) {
			var x row
			return x, r.Scan(&x.org, &x.kind)
		})
		return err
	}); err != nil || len(rows) == 0 {
		return err
	}
	unique := &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 20 * time.Hour}}
	var params []river.InsertManyParams
	for _, r := range rows {
		var args river.JobArgs = jobargs.SearchSyncOrg{OrgID: r.org}
		if r.kind == "ga4" {
			args = jobargs.AnalyticsSyncOrg{OrgID: r.org}
		}
		params = append(params, river.InsertManyParams{Args: args, InsertOpts: unique})
	}
	_, err := river.ClientFromContext[pgx.Tx](ctx).InsertMany(ctx, params)
	return err
}

// ranges plans the range jobs for a sync: the whole backfill window (newest chunk
// first, so recent data appears first) the first time, else the recent days.
func (s *Search) ranges(ctx context.Context, org, kind string) ([][2]time.Time, error) {
	today := s.Now().UTC().Truncate(24 * time.Hour)
	end := today.AddDate(0, 0, -1)
	var requested *time.Time
	err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT backfill_requested FROM sync_state WHERE kind = $1`, kind).Scan(&requested)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		if err != nil || requested != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO sync_state (org_id, kind, backfill_requested) VALUES ($1, $2, now())
			ON CONFLICT (org_id, kind) DO UPDATE SET backfill_requested = now()`, org, kind)
		return err
	})
	if err != nil {
		return nil, err
	}
	start := end.AddDate(0, 0, -(s.RefreshDays - 1))
	if requested == nil {
		start = end.AddDate(0, 0, -(s.BackfillDays - 1))
	}
	chunks := search.Chunks(start, end, s.ChunkDays)
	slices.Reverse(chunks)
	return chunks, nil
}

func (s *Search) enqueueRanges(ctx context.Context, chunks [][2]time.Time, mk func(from, to string) river.JobArgs) error {
	params := make([]river.InsertManyParams, len(chunks))
	for i, c := range chunks {
		params[i] = river.InsertManyParams{
			Args:       mk(c[0].Format(time.DateOnly), c[1].Format(time.DateOnly)),
			InsertOpts: &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 6 * time.Hour}, Priority: min(4, 1+i/4)},
		}
	}
	_, err := river.ClientFromContext[pgx.Tx](ctx).InsertMany(ctx, params)
	return err
}

type searchSyncOrgWorker struct {
	river.WorkerDefaults[jobargs.SearchSyncOrg]
	s *Search
}

func (w *searchSyncOrgWorker) Work(ctx context.Context, job *river.Job[jobargs.SearchSyncOrg]) error {
	org := job.Args.OrgID
	chunks, err := w.s.ranges(ctx, org, "search_console")
	if err != nil {
		return err
	}
	return w.s.enqueueRanges(ctx, chunks, func(from, to string) river.JobArgs {
		return jobargs.SearchSyncRange{OrgID: org, From: from, To: to}
	})
}

type analyticsSyncOrgWorker struct {
	river.WorkerDefaults[jobargs.AnalyticsSyncOrg]
	s *Search
}

func (w *analyticsSyncOrgWorker) Work(ctx context.Context, job *river.Job[jobargs.AnalyticsSyncOrg]) error {
	org := job.Args.OrgID
	chunks, err := w.s.ranges(ctx, org, "ga4")
	if err != nil {
		return err
	}
	return w.s.enqueueRanges(ctx, chunks, func(from, to string) river.JobArgs {
		return jobargs.AnalyticsSyncRange{OrgID: org, From: from, To: to}
	})
}

// ---- range syncs -------------------------------------------------------------------

func parseRange(from, to string) (time.Time, time.Time, error) {
	f, err1 := time.Parse(time.DateOnly, from)
	t, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil || t.Before(f) {
		return f, t, river.JobCancel(fmt.Errorf("bad range %s..%s", from, to))
	}
	return f, t, nil
}

type searchSyncRangeWorker struct {
	river.WorkerDefaults[jobargs.SearchSyncRange]
	s *Search
}

func (w *searchSyncRangeWorker) Timeout(*river.Job[jobargs.SearchSyncRange]) time.Duration {
	return 20 * time.Minute // background only; bounded by the chunk size
}

func (w *searchSyncRangeWorker) Work(ctx context.Context, job *river.Job[jobargs.SearchSyncRange]) error {
	s, org := w.s, job.Args.OrgID
	from, to, err := parseRange(job.Args.From, job.Args.To)
	if err != nil {
		return err
	}
	var site string
	if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		site, err = property(ctx, tx, "search_console")
		return err
	}); err != nil || site == "" {
		return err
	}
	c, err := s.client(ctx, org)
	if err != nil {
		return s.syncFailed(ctx, org, "search_console", err)
	}

	for _, day := range search.Days(from, to) {
		rows, err := c.DayRows(ctx, site, day)
		if err != nil {
			return s.syncFailed(ctx, org, "search_console", err)
		}
		if err := s.Warehouse.ReplaceSearchDay(ctx, org, day, rows); err != nil {
			return err
		}
	}
	totals, err := c.DayTotals(ctx, site, from, to)
	if err != nil {
		return s.syncFailed(ctx, org, "search_console", err)
	}
	detail, err := s.Warehouse.SearchDetailImpressions(ctx, org, from, to)
	if err != nil {
		return err
	}
	type month struct {
		start          time.Time
		queries, pages []warehouse.Rollup
	}
	var months []month
	for _, m := range search.Months(from, to) {
		first, last := warehouse.MonthBounds(m)
		q, p, err := s.Warehouse.SearchTop(ctx, org, first, last, search.TopQueries)
		if err != nil {
			return err
		}
		if len(p) > search.TopPages {
			p = p[:search.TopPages]
		}
		months = append(months, month{first, q, p})
	}

	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := search.UpsertDayTotals(ctx, tx, org, totals); err != nil {
			return err
		}
		for _, m := range months {
			if err := search.ReplaceSearchMonth(ctx, tx, org, m.start, m.queries, m.pages); err != nil {
				return err
			}
		}
		// Detail rows exclude anonymised queries, so they can be below the totals but
		// never meaningfully above them. Above means rows were double-loaded.
		for _, t := range totals {
			if d := detail[t.Date]; float64(d) > float64(t.Impressions)*1.01+10 {
				if _, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.ReconciliationFailed, Actor: "sync",
					Payload: map[string]any{"day": t.Date, "detail_impressions": d, "total_impressions": t.Impressions}}); err != nil {
					return err
				}
			}
		}
		if err := search.MarkSynced(ctx, tx, org, "search_console", to, nil); err != nil {
			return err
		}
		_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.SearchSynced, Actor: "sync",
			Payload: map[string]string{"from": job.Args.From, "to": job.Args.To}})
		return err
	})
}

type analyticsSyncRangeWorker struct {
	river.WorkerDefaults[jobargs.AnalyticsSyncRange]
	s *Search
}

func (w *analyticsSyncRangeWorker) Timeout(*river.Job[jobargs.AnalyticsSyncRange]) time.Duration {
	return 20 * time.Minute
}

func (w *analyticsSyncRangeWorker) Work(ctx context.Context, job *river.Job[jobargs.AnalyticsSyncRange]) error {
	s, org := w.s, job.Args.OrgID
	from, to, err := parseRange(job.Args.From, job.Args.To)
	if err != nil {
		return err
	}
	var prop string
	if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		prop, err = property(ctx, tx, "ga4")
		return err
	}); err != nil || prop == "" {
		return err
	}
	c, err := s.client(ctx, org)
	if err != nil {
		return s.syncFailed(ctx, org, "ga4", err)
	}
	type day struct {
		d                    time.Time
		channels, assistants []search.ChannelDay
	}
	var days []day
	for _, d := range search.Days(from, to) {
		rows, err := c.AnalyticsDay(ctx, prop, d)
		if err != nil {
			return s.syncFailed(ctx, org, "ga4", err)
		}
		if err := s.Warehouse.ReplaceAnalyticsDay(ctx, org, d, rows); err != nil {
			return err
		}
		ch, as := search.AggregateAnalytics(rows)
		days = append(days, day{d, ch, as})
	}
	type month struct {
		start time.Time
		rows  []warehouse.Rollup
	}
	var months []month
	for _, m := range search.Months(from, to) {
		first, last := warehouse.MonthBounds(m)
		rows, err := s.Warehouse.LandingTop(ctx, org, first, last, search.TopLanding)
		if err != nil {
			return err
		}
		months = append(months, month{first, rows})
	}
	return db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		for _, d := range days {
			if err := search.ReplaceAnalyticsDay(ctx, tx, org, d.d, d.channels, d.assistants); err != nil {
				return err
			}
		}
		for _, m := range months {
			if err := search.ReplaceLandingMonth(ctx, tx, org, m.start, m.rows); err != nil {
				return err
			}
		}
		if err := search.MarkSynced(ctx, tx, org, "ga4", to, nil); err != nil {
			return err
		}
		_, err := s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.AnalyticsSynced, Actor: "sync",
			Payload: map[string]string{"from": job.Args.From, "to": job.Args.To}})
		return err
	})
}

// syncFailed handles a Google error: a dead grant breaks the connection (visibly) and
// stops retrying; anything else is retried by River.
func (s *Search) syncFailed(ctx context.Context, org, kind string, err error) error {
	if !errors.Is(err, google.ErrAuth) {
		return err
	}
	if err := db.InTenant(ctx, s.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return search.MarkSyncError(ctx, tx, org, kind, err)
	}); err != nil {
		return err
	}
	return s.markBroken(ctx, org, reconnect)
}
