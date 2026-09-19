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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/api"
	"github.com/UncleSon21/vellatry/internal/config"
	"github.com/UncleSon21/vellatry/internal/dataforseo"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/gateway/claude"
	"github.com/UncleSon21/vellatry/internal/platform/gateway/pgcache"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/platform/metering"
	"github.com/UncleSon21/vellatry/internal/visibility/judge"
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

	srv := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: (&api.Server{
			Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...),
			Verifier: verifier, Hub: hub, Logger: log, AllowedOrigins: cfg.AllowedOrigins,
		}).Handler(),
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
	if cfg.DataForSEOLogin != "" && cfg.DataForSEOPassword != "" {
		client, err := dataforseo.New(dataforseo.Config{
			Login: cfg.DataForSEOLogin, Password: cfg.DataForSEOPassword,
			Budget: &budget.Daily{LimitUSD: cfg.DataForSEODailyUSD, EstimateUSD: 0.02},
		})
		if err != nil {
			return err
		}
		vis.Answers = client
	} else {
		log.Warn("DATAFORSEO_LOGIN/PASSWORD not set: the Visibility engine will not request answers")
	}

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
		if cfg.AllowJudge {
			vis.Judge = gw
		} else {
			log.Info("the per-item judge is off (set VELLATRY_ALLOW_JUDGE=1 during the design-partner phase)")
		}
	}
	vis.Register(ws)
	periodic = append(periodic, vis.PeriodicJobs()...)

	client, err := jobs.NewWorker(pool, ws, jobs.Options{PeriodicJobs: periodic, Logger: log})
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return err
	}
	log.Info("worker started", "visibility", vis.Answers != nil, "judge", vis.Judge != nil)
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.Stop(stopCtx)
}
