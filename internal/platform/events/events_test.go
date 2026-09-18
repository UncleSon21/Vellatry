package events_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

type notifyArgs struct {
	EventID int64 `json:"event_id"`
}

func (notifyArgs) Kind() string { return "test_notify" }

func TestEmitWritesEventAndSubscriberJobsAtomically(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	orgA, _ := testdb.NewOrg(t, pool, "events-a")
	orgB, _ := testdb.NewOrg(t, pool, "events-b")

	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, events.Subscription{
		Name:  "notify",
		Kinds: []string{"brand.updated"},
		Job:   func(s events.Stored) river.JobArgs { return notifyArgs{EventID: s.ID} },
	})

	var committed events.Stored
	err = db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
		committed, err = bus.Emit(ctx, tx, orgA, events.Event{Kind: "brand.updated", Actor: "test", Payload: map[string]string{"field": "aliases"}})
		if err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(ctx, "SELECT current_user").Scan(&role); err != nil {
			return err
		}
		if role != "vellatry_tenant" {
			t.Errorf("role after Emit = %q, want vellatry_tenant", role)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := jobsFor(t, pool, committed.ID); n != 1 {
		t.Errorf("committed event has %d subscriber jobs, want 1", n)
	}

	// Rolled back: neither the event nor its job may exist.
	var rolledBack events.Stored
	boom := errors.New("boom")
	err = db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
		rolledBack, err = bus.Emit(ctx, tx, orgA, events.Event{Kind: "brand.updated"})
		if err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want boom", err)
	}
	if n := jobsFor(t, pool, rolledBack.ID); n != 0 {
		t.Errorf("rolled-back event left %d jobs", n)
	}

	// Unsubscribed kinds create no jobs, and tenants cannot see each other's events.
	err = db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := bus.Emit(ctx, tx, orgA, events.Event{Kind: "unrelated.kind"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var visible int
	err = db.InTenant(ctx, pool, orgB, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM events WHERE org_id = $1", orgA).Scan(&visible)
	})
	if err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Errorf("org B saw %d of org A's events", visible)
	}

	// A tenant transaction cannot emit an event for another org.
	err = db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := bus.Emit(ctx, tx, orgB, events.Event{Kind: "brand.updated"})
		return err
	})
	if err == nil {
		t.Error("org A emitted an event for org B")
	}
}

func jobsFor(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, eventID int64) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM river_job WHERE kind = 'test_notify' AND args->>'event_id' = $1",
		strconv.FormatInt(eventID, 10)).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
