// Package halt stops the worker when Vellatry's own account with a paid service is
// dead: no credit, a rejected key, a revoked token.
//
// Without this, every queued job discovers the same dead account on its own and burns
// its retries doing it. A tenant's keyword run would grind through hundreds of jobs,
// each failing the same way, and the one fact worth knowing (the account needs paying)
// would be buried in hundreds of identical errors.
//
// A halt is deliberately only for credentials that are *ours* and account-wide:
// DataForSEO, the model gateway. A customer's own broken Google or Asana connection
// affects that tenant alone, is already shown to them with a fix, and must never stop
// work for everyone else.
//
// Nothing is lost while halted: jobs are left queued, not failed, and clearing the halt
// (`vellatry resume`) lets them run. Clearing is deliberately manual: an automatic retry
// would just rediscover the dead account, which is what this exists to stop.
package halt

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fatal marks an error as "our account with this service cannot be used at all". The
// clients raise it; the worker's middleware turns it into a halt.
type Fatal struct {
	Service string // dataforseo, anthropic
	Reason  string // what the service said, trimmed to something a person can read
	Err     error
}

func (f *Fatal) Error() string {
	return fmt.Sprintf("%s: account cannot be used: %s", f.Service, f.Reason)
}

func (f *Fatal) Unwrap() error { return f.Err }

// Account wraps err as account-fatal.
func Account(service, reason string, err error) error {
	return &Fatal{Service: service, Reason: reason, Err: err}
}

// IsFatal reports whether err (or anything it wraps) is account-fatal.
func IsFatal(err error) (*Fatal, bool) {
	var f *Fatal
	ok := errors.As(err, &f)
	return f, ok
}

// State is the halt as stored. Since is zero when the worker is running.
type State struct {
	Halted  bool
	Service string
	Reason  string
	Since   time.Time
}

// Trip records a halt. The first one wins: later jobs failing the same way do not
// overwrite the reason, which is the one that explains it.
func Trip(ctx context.Context, pool *pgxpool.Pool, f *Fatal) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO worker_halt (id, service, reason, halted_at) VALUES (TRUE, $1, $2, now())
		ON CONFLICT (id) DO NOTHING`, f.Service, f.Reason)
	return err
}

// Current reports whether the worker is halted.
func Current(ctx context.Context, pool *pgxpool.Pool) (State, error) {
	var s State
	err := pool.QueryRow(ctx, `SELECT service, reason, halted_at FROM worker_halt WHERE id`).Scan(&s.Service, &s.Reason, &s.Since)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	s.Halted = true
	return s, nil
}

// Clear lets the worker run again. It returns what was cleared, so the caller can say
// what it just released.
func Clear(ctx context.Context, pool *pgxpool.Pool) (State, error) {
	was, err := Current(ctx, pool)
	if err != nil || !was.Halted {
		return was, err
	}
	_, err = pool.Exec(ctx, `DELETE FROM worker_halt WHERE id`)
	return was, err
}
