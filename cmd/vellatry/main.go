// Command vellatry runs one role of the service: migrate, api or worker.
//
//	vellatry migrate   apply database migrations
//	vellatry api       serve the dashboard API (reads stored results, enqueues work)
//	vellatry worker    run jobs (answers, judging, planning)
//
// Configuration comes from environment variables; see internal/config and .env.example.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // schedules use Australian time zones even where the OS has no zone database

	"cloud.google.com/go/bigquery"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/api"
	"github.com/UncleSon21/vellatry/internal/asanaauth"
	"github.com/UncleSon21/vellatry/internal/config"
	"github.com/UncleSon21/vellatry/internal/dataforseo"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/email"
	"github.com/UncleSon21/vellatry/internal/googleauth"
	"github.com/UncleSon21/vellatry/internal/pdf"
	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/gateway/claude"
	"github.com/UncleSon21/vellatry/internal/platform/gateway/pgcache"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/platform/metering"
	"github.com/UncleSon21/vellatry/internal/platform/secrets"
	"github.com/UncleSon21/vellatry/internal/site"
	"github.com/UncleSon21/vellatry/internal/visibility/judge"
	"github.com/UncleSon21/vellatry/internal/warehouse"
	"github.com/UncleSon21/vellatry/internal/workers"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: vellatry migrate|api|worker")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1], log); err != nil {
		log.Error("exit", "role", os.Args[1], "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, role string, log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch role {
	case "migrate":
		if err := db.Migrate(ctx, pool); err != nil {
			return err
		}
		log.Info("migrations applied")
		return nil
	case "api":
		return runAPI(ctx, cfg, pool, log)
	case "worker":
		return runWorker(ctx, cfg, pool, log)
	}
	return fmt.Errorf("unknown role %q (want migrate, api or worker)", role)
}

func runAPI(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) error {
	var verifier api.Verifier
	switch {
	case cfg.ClerkIssuer != "":
		verifier = &api.ClerkVerifier{Issuer: cfg.ClerkIssuer, JWKSURL: cfg.ClerkJWKSURL, AuthorizedParties: cfg.ClerkAuthorizedParties}
	case cfg.DevAuth:
		log.Warn("VELLATRY_DEV_AUTH=1: requests are trusted by X-Dev-Email header. Never enable this in production.")
		verifier = api.DevVerifier{}
	default:
		return errors.New("no authentication configured: set CLERK_ISSUER, or VELLATRY_DEV_AUTH=1 for local development")
	}
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		return err
	}
	hub := &api.Hub{Pool: pool, Logger: log}
	go hub.Run(ctx)

	server := &api.Server{
		Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...),
		Verifier: verifier, Hub: hub, Logger: log, AllowedOrigins: cfg.AllowedOrigins, AppURL: cfg.AppURL, HubURL: cfg.HubURL,
	}
	box, err := openBox(cfg, log)
	if err != nil {
		return err
	}
	server.Box = box
	if cfg.GoogleConfigured() {
		server.GoogleOAuth = googleauth.Config(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL)
	} else {
		log.Warn("Google connections disabled: set GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, GOOGLE_REDIRECT_URL and VELLATRY_SECRET_KEY")
	}
	if cfg.AsanaConfigured() {
		server.AsanaOAuth = asanaauth.Config(cfg.AsanaClientID, cfg.AsanaClientSecret, cfg.AsanaRedirectURL)
	} else {
		log.Warn("Asana disabled: set ASANA_CLIENT_ID, ASANA_CLIENT_SECRET, ASANA_REDIRECT_URL and VELLATRY_SECRET_KEY")
	}
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second, // the event stream lifts this for itself
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("api listening", "port", cfg.Port)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runWorker(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) error {
	ws := river.NewWorkers()
	river.AddWorker(ws, &jobs.HeartbeatWorker{Logger: log})
	periodic := []*river.PeriodicJob{jobs.HeartbeatEvery(10 * time.Minute)}

	vis := &workers.Visibility{
		Pool: pool, Logger: log,
		Bus:       events.NewBus(nil, domainevents.Subscriptions()...),
		JudgeName: cfg.CheapModel + "/" + judge.Version,
	}
	var researchAPI workers.KeywordsAPI
	if cfg.DataForSEOLogin != "" && cfg.DataForSEOPassword != "" {
		client, err := dataforseo.New(dataforseo.Config{
			Login: cfg.DataForSEOLogin, Password: cfg.DataForSEOPassword,
			Budget: &budget.Daily{LimitUSD: cfg.DataForSEODailyUSD, EstimateUSD: 0.02},
		})
		if err != nil {
			return err
		}
		vis.Answers = client
		// Keyword research has its own daily cap and its own per-unit estimate: one
		// search-result task costs a fraction of an AI answer, and sharing a budget
		// would make every reservation the size of the dearest call.
		researchClient, err := dataforseo.New(dataforseo.Config{
			Login: cfg.DataForSEOLogin, Password: cfg.DataForSEOPassword,
			Budget: &budget.Daily{LimitUSD: cfg.KeywordsDailyUSD, EstimateUSD: 0.0006},
		})
		if err != nil {
			return err
		}
		researchAPI = researchClient
	} else {
		log.Warn("DATAFORSEO_LOGIN/PASSWORD not set: the Visibility engine will not request answers")
	}

	var drafter workers.Completer // the report summary suggestion; nil without an API key
	if cfg.AnthropicAPIKey != "" {
		var allow []string
		if cfg.AllowJudge {
			allow = []string{"judge"}
		}
		gw, err := gateway.New(gateway.Config{
			Purposes:     gateway.DefaultPurposes(),
			AllowPerItem: allow,
			Models:       map[string]string{"cheap": cfg.CheapModel, "strong": cfg.StrongModel},
			Provider:     claude.New(cfg.AnthropicAPIKey, nil),
			Cache:        pgcache.Cache{Pool: pool},
			Budgets:      &metering.DailyBudgets{Pool: pool, Provider: "anthropic", LimitUSD: cfg.LLMDailyUSDTenant, EstimateUSD: 0.01, Logger: log},
			Meter:        metering.GatewayMeter{Pool: pool, Provider: "anthropic"},
		})
		if err != nil {
			return err
		}
		drafter = gw
		if cfg.AllowJudge {
			vis.Judge = gw
		} else {
			log.Info("the per-item judge is off (set VELLATRY_ALLOW_JUDGE=1 during the design-partner phase)")
		}
	}
	vis.Register(ws)
	periodic = append(periodic, vis.PeriodicJobs()...)

	siteJobs := &workers.Site{Pool: pool, Crawler: &site.Crawler{MaxPages: 300, Concurrency: 3, Delay: 250 * time.Millisecond}, Logger: log,
		Bus: events.NewBus(nil, domainevents.Subscriptions()...)}
	siteJobs.Register(ws)
	periodic = append(periodic, siteJobs.PeriodicJobs()...)

	box, err := openBox(cfg, log)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		return fmt.Errorf("VELLATRY_TIME_ZONE: %w", err)
	}
	auto := &workers.Automation{
		Pool: pool, Logger: log, Box: box, AppURL: cfg.AppURL, Location: loc,
		Bus:   events.NewBus(nil, domainevents.Subscriptions()...),
		Email: openEmail(cfg, log),
	}
	auto.Register(ws)
	periodic = append(periodic, auto.PeriodicJobs()...)

	reportJobs := &workers.Reports{
		Pool: pool, Logger: log, Notify: auto, Email: auto.Email, Drafter: drafter, HubURL: cfg.HubURL, Location: loc,
		Bus: events.NewBus(nil, domainevents.Subscriptions()...),
	}
	if cfg.GotenbergURL != "" {
		reportJobs.PDF = pdf.Gotenberg{URL: cfg.GotenbergURL}
	} else {
		log.Warn("GOTENBERG_URL not set: published reports have no PDF (the web view is unaffected)")
	}
	reportJobs.Register(ws)
	periodic = append(periodic, reportJobs.PeriodicJobs()...)

	wh, closeWH, err := openWarehouse(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeWH()

	research := &workers.Keywords{
		Pool: pool, Logger: log, Notify: auto, Research: researchAPI, Warehouse: wh,
		Bus: events.NewBus(nil, domainevents.Subscriptions()...),
	}
	research.Register(ws)
	periodic = append(periodic, research.PeriodicJobs()...)
	if researchAPI == nil {
		log.Warn("keyword research disabled: DATAFORSEO_LOGIN/PASSWORD not set")
	}

	if cfg.AsanaConfigured() {
		tasks := &workers.Asana{
			Pool: pool, Box: box, Logger: log, AppURL: cfg.AppURL,
			OAuth: asanaauth.Config(cfg.AsanaClientID, cfg.AsanaClientSecret, cfg.AsanaRedirectURL),
			Bus:   events.NewBus(nil, domainevents.Subscriptions()...),
		}
		tasks.Register(ws)
	} else {
		log.Warn("Asana disabled: Asana OAuth or VELLATRY_SECRET_KEY not configured")
	}

	if cfg.GoogleConfigured() {
		sync := &workers.Search{
			Pool: pool, Box: box, Warehouse: wh, Logger: log,
			OAuth: googleauth.Config(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL),
			Bus:   events.NewBus(nil, domainevents.Subscriptions()...),
		}
		sync.Register(ws)
		periodic = append(periodic, sync.PeriodicJobs()...)
	} else {
		log.Warn("Google syncs disabled: Google OAuth or VELLATRY_SECRET_KEY not configured")
	}

	client, err := jobs.NewWorker(pool, ws, jobs.Options{PeriodicJobs: periodic, Logger: log})
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return err
	}
	log.Info("worker started", "visibility", vis.Answers != nil, "judge", vis.Judge != nil, "google", cfg.GoogleConfigured(),
		"keywords", researchAPI != nil, "asana", cfg.AsanaConfigured(), "email", cfg.EmailConfigured(), "pdf", cfg.GotenbergURL != "")
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.Stop(stopCtx)
}

// openBox returns the credential box, or nil when VELLATRY_SECRET_KEY is unset (every
// feature that stores a credential is then disabled).
func openBox(cfg config.Config, log *slog.Logger) (*secrets.Box, error) {
	if cfg.SecretKey == "" {
		log.Warn("VELLATRY_SECRET_KEY not set: Google, Asana and Slack destinations are disabled")
		return nil, nil
	}
	return secrets.NewBox(cfg.SecretKey)
}

// openEmail returns Postmark when configured. Without it, email is only logged, and only
// in local development: sign-in links must never land in production logs.
func openEmail(cfg config.Config, log *slog.Logger) email.Sender {
	switch {
	case cfg.EmailConfigured():
		return &email.Postmark{Token: cfg.PostmarkToken, From: cfg.EmailFrom, Stream: cfg.PostmarkStream}
	case cfg.DevAuth:
		log.Warn("POSTMARK_SERVER_TOKEN/EMAIL_FROM not set: email is written to the log instead of sent (development only)")
		return email.Log{Logger: log}
	}
	log.Warn("email disabled: set POSTMARK_SERVER_TOKEN and EMAIL_FROM")
	return nil
}

// openWarehouse returns BigQuery when configured. Without it, raw facts are kept in
// memory, which is only acceptable for local development.
func openWarehouse(ctx context.Context, cfg config.Config, log *slog.Logger) (warehouse.Warehouse, func(), error) {
	if cfg.BigQueryProject == "" {
		log.Warn("BIGQUERY_PROJECT not set: raw Search Console and GA4 facts are kept IN MEMORY and lost on restart. Local development only.")
		return warehouse.NewMemory(), func() {}, nil
	}
	client, err := bigquery.NewClient(ctx, cfg.BigQueryProject)
	if err != nil {
		return nil, nil, err
	}
	client.Location = cfg.BigQueryLocation
	bq := &warehouse.BigQuery{
		Client: client, Dataset: cfg.BigQueryDataset, MaxBytesBilled: cfg.BigQueryMaxBytes,
		Retention: time.Duration(cfg.SearchRetentionDays) * 24 * time.Hour,
	}
	if err := bq.EnsureDataset(ctx, cfg.BigQueryLocation); err != nil {
		client.Close()
		return nil, nil, err
	}
	return bq, func() { client.Close() }, nil
}
