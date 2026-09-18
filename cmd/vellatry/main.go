// Command vellatry runs one role of the service: migrate, api or worker.
//
//	DATABASE_URL=postgres://... vellatry migrate
//	DATABASE_URL=postgres://... PORT=8080 vellatry api
//	DATABASE_URL=postgres://... vellatry worker
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

	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/api"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
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
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}
	pool, err := db.Open(ctx, url)
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
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		srv := &http.Server{
			Addr:              ":" + port,
			Handler:           (&api.Server{Pool: pool}).Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      30 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		}()
		log.Info("api listening", "port", port)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil

	case "worker":
		workers := river.NewWorkers()
		river.AddWorker(workers, &jobs.HeartbeatWorker{Logger: log})
		client, err := jobs.NewWorker(pool, workers, jobs.Options{
			PeriodicJobs: []*river.PeriodicJob{jobs.HeartbeatEvery(10 * time.Minute)},
			Logger:       log,
		})
		if err != nil {
			return err
		}
		if err := client.Start(ctx); err != nil {
			return err
		}
		log.Info("worker started")
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return client.Stop(stopCtx)
	}
	return fmt.Errorf("unknown role %q (want migrate, api or worker)", role)
}
