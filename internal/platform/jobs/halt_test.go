package jobs

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/UncleSon21/vellatry/internal/platform/halt"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// The middleware is what makes the halt real: it refuses to start work while one is
// set, and sets one the first time a job proves an account is unusable.
func TestHaltMiddleware(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = halt.Clear(ctx, pool) })
	if _, err := halt.Clear(ctx, pool); err != nil {
		t.Fatal(err)
	}

	mw := haltMiddleware(pool, slog.Default())
	row := &rivertype.JobRow{ID: 1, Kind: "visibility_collect"}

	// An ordinary failure is passed through and changes nothing.
	ran := false
	err := mw(ctx, row, func(context.Context) error {
		ran = true
		return errors.New("the engine timed out")
	})
	if !ran || err == nil {
		t.Fatalf("an ordinary job should run and report its error: ran=%v err=%v", ran, err)
	}
	if s, _ := halt.Current(ctx, pool); s.Halted {
		t.Fatal("a timeout halted the worker")
	}

	// A job that proves the account is unusable trips the halt, and still reports its
	// own error so River records the failure.
	accountErr := halt.Account("dataforseo", "the account is out of credit", errors.New("40200"))
	if err := mw(ctx, row, func(context.Context) error { return accountErr }); err == nil {
		t.Error("the job's error should still be returned")
	}
	s, err := halt.Current(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Halted || s.Service != "dataforseo" {
		t.Fatalf("the halt was not recorded: %+v", s)
	}

	// Now nothing starts: the next job is snoozed, not run and not failed.
	ran = false
	err = mw(ctx, row, func(context.Context) error {
		ran = true
		return nil
	})
	if ran {
		t.Error("a job ran while the worker was halted")
	}
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) {
		t.Errorf("a halted job should be snoozed so the work is kept, got %v", err)
	}

	// Once someone has fixed the account and cleared the halt, work starts again. The
	// middleware caches the state briefly, so this reads it fresh.
	if _, err := halt.Clear(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fresh := haltMiddleware(pool, slog.Default())
	ran = false
	if err := fresh(ctx, row, func(context.Context) error {
		ran = true
		return nil
	}); err != nil || !ran {
		t.Errorf("after resuming, jobs should run: ran=%v err=%v", ran, err)
	}
}
