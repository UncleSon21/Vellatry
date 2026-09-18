// Package jobs configures the River job queue.
//
// The worker role runs a full client; the api role gets an insert-only client, so it can
// enqueue work without ever running it (and without importing the packages that call
// external services).
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Client is the queue client used across Vellatry.
type Client = river.Client[pgx.Tx]

// Options configures a working client.
type Options struct {
	MaxWorkers   int           // per process, default 20
	JobTimeout   time.Duration // default 5 minutes; long external work is split into jobs, not stretched
	MaxAttempts  int           // default 8
	PeriodicJobs []*river.PeriodicJob
	Logger       *slog.Logger
}

// NewWorker returns a client that works jobs with the registered workers.
func NewWorker(pool *pgxpool.Pool, workers *river.Workers, o Options) (*Client, error) {
	if o.MaxWorkers <= 0 {
		o.MaxWorkers = 20
	}
	if o.JobTimeout <= 0 {
		o.JobTimeout = 5 * time.Minute
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 8
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: o.MaxWorkers}},
		Workers:      workers,
		JobTimeout:   o.JobTimeout,
		MaxAttempts:  o.MaxAttempts,
		PeriodicJobs: o.PeriodicJobs,
		Logger:       o.Logger,
	})
}

// NewInserter returns a client that can enqueue jobs but never works them.
func NewInserter(pool *pgxpool.Pool) (*Client, error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{})
}

// HeartbeatArgs is a periodic job that proves the worker is alive and draining.
type HeartbeatArgs struct{}

func (HeartbeatArgs) Kind() string { return "heartbeat" }

// HeartbeatWorker logs each heartbeat.
type HeartbeatWorker struct {
	river.WorkerDefaults[HeartbeatArgs]
	Logger *slog.Logger
}

func (w *HeartbeatWorker) Work(ctx context.Context, job *river.Job[HeartbeatArgs]) error {
	w.Logger.InfoContext(ctx, "worker heartbeat", "job_id", job.ID)
	return nil
}

// HeartbeatEvery schedules a heartbeat job.
func HeartbeatEvery(d time.Duration) *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(d), func() (river.JobArgs, *river.InsertOpts) {
		return HeartbeatArgs{}, nil
	}, &river.PeriodicJobOpts{RunOnStart: true})
}
