package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/UncleSon21/vellatry/internal/platform/halt"
)

// SnoozeWhileHalted is how long a job waits before looking again. Long enough that a
// halted worker is quiet, short enough that work resumes soon after someone clears it.
const SnoozeWhileHalted = 5 * time.Minute

// haltCheck reads the halt state at most once every few seconds. Every job would
// otherwise ask the database the same question.
type haltCheck struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	mu      sync.Mutex
	state   halt.State
	checked time.Time
}

const haltCacheFor = 5 * time.Second

func (h *haltCheck) current(ctx context.Context) halt.State {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.checked) < haltCacheFor {
		return h.state
	}
	s, err := halt.Current(ctx, h.pool)
	if err != nil {
		// Unknown: let the job run. A database that cannot answer this is a bigger
		// problem, and refusing every job would turn it into an outage of our own.
		h.logger.ErrorContext(ctx, "could not read the worker halt", "error", err)
		return halt.State{}
	}
	h.state, h.checked = s, time.Now()
	return s
}

func (h *haltCheck) trip(ctx context.Context, f *halt.Fatal) {
	if err := halt.Trip(ctx, h.pool, f); err != nil {
		h.logger.ErrorContext(ctx, "could not record the worker halt", "error", err, "service", f.Service)
		return
	}
	h.mu.Lock()
	h.state, h.checked = halt.State{Halted: true, Service: f.Service, Reason: f.Reason, Since: time.Now()}, time.Now()
	h.mu.Unlock()
	h.logger.ErrorContext(ctx, "worker halted: an account Vellatry pays for cannot be used, so every job would fail the same way",
		"service", f.Service, "reason", f.Reason, "resume_with", "vellatry resume")
}

// haltMiddleware keeps the queue still while one of Vellatry's own paid accounts is
// unusable, and sets that halt the first time a job proves it.
func haltMiddleware(pool *pgxpool.Pool, logger *slog.Logger) river.WorkerMiddlewareFunc {
	check := &haltCheck{pool: pool, logger: logger}
	return func(ctx context.Context, job *rivertype.JobRow, doInner func(ctx context.Context) error) error {
		if s := check.current(ctx); s.Halted {
			// Snoozed, not failed: the work is still wanted once someone pays or fixes
			// the key, and a failed job would burn an attempt for nothing.
			return river.JobSnooze(SnoozeWhileHalted)
		}
		err := doInner(ctx)
		if f, ok := halt.IsFatal(err); ok {
			check.trip(ctx, f)
		}
		return err
	}
}
