package workers

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/dataforseo"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
	"github.com/UncleSon21/vellatry/internal/visibility/engine"
	"github.com/UncleSon21/vellatry/internal/visibility/store"
)

// A plan job that dies between creating its tasks and posting them — a crash, a
// timeout, the database going away — leaves them pending, and a pending task counts as
// in flight. Nothing else rescues them: the planner asks each question only for the
// answers it still needs, so the question is never asked on that engine again, and
// retrying the job does not help because the retry plans against the same count.
func TestPlanReleasesTasksADeadJobLeftPending(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org := screeningOrg(t, pool, "plan-stranded")
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, domainevents.Subscriptions()...)
	src := &scriptedEngines{}
	w := &planOrgWorker{v: &Visibility{Pool: pool, Answers: src, Bus: bus, Logger: slog.Default()}}

	// A first plan that posts nothing: the tasks are created and the job dies there.
	// Making the post fail outright would mark them failed, which is the behaviour this
	// is about missing, so the job is stopped by hand instead.
	src.mu.Lock()
	src.refuse = func([]dataforseo.Request) ([]dataforseo.Posted, error) {
		return nil, fmt.Errorf("the job died here")
	}
	src.mu.Unlock()
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		settings, err := store.LoadSettings(ctx, tx)
		if err != nil {
			return err
		}
		in, err := store.LoadPlanInput(ctx, tx, settings)
		if err != nil {
			return err
		}
		_, err = store.CreateTasks(ctx, tx, org, engine.Plan(in))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, stranded := taskStates(t, pool, org)
	if len(stranded) == 0 {
		t.Fatal("the test did not manage to strand anything")
	}

	// While they are recent they are genuinely in flight, so the next plan leaves them
	// alone: a job posting right now must not have its tasks taken away.
	if err := w.Work(ctx, job(jobargs.VisibilityPlanOrg{OrgID: org})); err != nil {
		t.Fatal(err)
	}
	if states, pending := taskStates(t, pool, org); len(pending) != len(stranded) {
		t.Errorf("a plan released tasks that were still in flight: %v (all: %v)", pending, states)
	}
	if n := len(src.batches()); n != 0 {
		t.Errorf("%d batches posted, want none: every question was already in flight", n)
	}

	// Older than a job can live, they are stranded by definition.
	age(t, pool, org, store.StrandedAfter)
	src.mu.Lock()
	src.refuse = func([]dataforseo.Request) ([]dataforseo.Posted, error) {
		return nil, &dataforseo.APIError{HTTPStatus: 200, StatusCode: 40501, Message: "invalid field"}
	}
	src.mu.Unlock()
	if err := w.Work(ctx, job(jobargs.VisibilityPlanOrg{OrgID: org})); err != nil {
		t.Fatal(err)
	}
	states, pending := taskStates(t, pool, org)
	if len(pending) != 0 {
		t.Errorf("tasks still pending on %v (all: %v): their questions are never asked again", pending, states)
	}
	// Every engine the dead job stranded is asked again, and the row says what happened.
	asked := map[dataforseo.Engine]bool{}
	for _, e := range src.batches() {
		asked[e] = true
	}
	if len(asked) != len(stranded) {
		t.Errorf("the next plan asked %d engines, want the %d that were stranded", len(asked), len(stranded))
	}
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var reason string
		if err := tx.QueryRow(ctx, `SELECT error FROM answer_tasks WHERE error LIKE 'never posted%' LIMIT 1`).Scan(&reason); err != nil {
			return err
		}
		if reason == "" {
			t.Error("a released task does not say why it failed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A task that was posted is never released, however old: it is waiting on DataForSEO,
// and the collector gives up on it by its own rules.
func TestExpireStrandedLeavesPostedTasksAlone(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org := screeningOrg(t, pool, "stranded-queued")

	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		settings, err := store.LoadSettings(ctx, tx)
		if err != nil {
			return err
		}
		in, err := store.LoadPlanInput(ctx, tx, settings)
		if err != nil {
			return err
		}
		tasks, err := store.CreateTasks(ctx, tx, org, engine.Plan(in))
		if err != nil {
			return err
		}
		// One posted, the rest left as a dead job would leave them.
		return store.MarkQueued(ctx, tx, tasks[0].ID, "task-1", 0.002)
	}); err != nil {
		t.Fatal(err)
	}
	age(t, pool, org, store.StrandedAfter)

	var released int
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		released, err = store.ExpireStranded(ctx, tx, store.StrandedAfter)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	states, pending := taskStates(t, pool, org)
	if released == 0 || len(pending) != 0 {
		t.Errorf("released %d, still pending %v", released, pending)
	}
	if states["queued"] != 1 {
		t.Errorf("task states = %v; the posted one should still be queued", states)
	}
}

// age moves an organisation's pending tasks back in time, so the test does not have to
// wait out a threshold measured in half-hours.
func age(t *testing.T, pool *pgxpool.Pool, org string, by time.Duration) {
	t.Helper()
	if err := db.InTenant(context.Background(), pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE answer_tasks SET created_at = created_at - $1::interval WHERE status = 'pending'`, by+time.Minute)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
