package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/brand"
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

// screeningOrg makes an organisation with a brand, a competitor and one question, which
// the next plan screens twice on each of the three engines.
func screeningOrg(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	org, brandID := testdb.NewOrg(t, pool, name)
	err := db.InTenant(context.Background(), pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := brand.AddCompetitor(ctx, tx, org, brandID, brand.Competitor{Name: "Ecosa", Domains: []string{"ecosa.com.au"}}); err != nil {
			return err
		}
		var topicID string
		if err := tx.QueryRow(ctx, `INSERT INTO topics (org_id, brand_id, name, demand_monthly) VALUES ($1, $2, 'mattress', 5000) RETURNING id::text`,
			org, brandID).Scan(&topicID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO prompts (org_id, brand_id, topic_id, text, source) VALUES ($1, $2, $3, $4, 'template')`,
			org, brandID, topicID, bestMattress)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return org
}

// taskStates counts an organisation's answer tasks by status, and by engine for those
// left pending.
func taskStates(t *testing.T, pool *pgxpool.Pool, org string) (map[string]int, map[string]int) {
	t.Helper()
	byStatus, pending := map[string]int{}, map[string]int{}
	err := db.InTenant(context.Background(), pool, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT status, engine FROM answer_tasks`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var status, eng string
			if err := rows.Scan(&status, &eng); err != nil {
				return err
			}
			byStatus[status]++
			if status == "pending" {
				pending[eng]++
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return byStatus, pending
}

// scriptedEngines stands in for DataForSEO, answering each collection the way the test
// scripted it and each post with what refuse returns.
type scriptedEngines struct {
	mu     sync.Mutex
	script map[string]func() (dataforseo.Answer, bool, error)
	refuse func(reqs []dataforseo.Request) ([]dataforseo.Posted, error)
	posted []dataforseo.Engine // one entry per batch, in order
}

func (s *scriptedEngines) Live(context.Context, dataforseo.Request) (dataforseo.Answer, error) {
	return dataforseo.Answer{}, errors.New("live answers are not part of this flow")
}

func (s *scriptedEngines) PostTasks(_ context.Context, reqs []dataforseo.Request) ([]dataforseo.Posted, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posted = append(s.posted, reqs[0].Engine)
	return s.refuse(reqs)
}

func (s *scriptedEngines) GetTask(_ context.Context, e dataforseo.Engine, id string) (dataforseo.Answer, bool, error) {
	s.mu.Lock()
	f := s.script[id]
	s.mu.Unlock()
	if f == nil {
		return dataforseo.Answer{}, false, fmt.Errorf("no %s task %q was posted", e, id)
	}
	return f()
}

func (s *scriptedEngines) batches() []dataforseo.Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dataforseo.Engine(nil), s.posted...)
}

// TestCollectWhenDataForSEOFails covers every way collecting an answer can go wrong: a
// passing outage is retried, a permanent failure is recorded and not retried, a task that
// is not ready waits and is eventually given up on by name, and a job that runs twice
// records its answer once.
func TestCollectWhenDataForSEOFails(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org := screeningOrg(t, pool, "collect-failures")
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, domainevents.Subscriptions()...)
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	// The plan's six tasks. All but the last were posted; the last never got a task id.
	var tasks []store.Task
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		s, err := store.LoadSettings(ctx, tx)
		if err != nil {
			return err
		}
		in, err := store.LoadPlanInput(ctx, tx, s)
		if err != nil {
			return err
		}
		tasks, err = store.CreateTasks(ctx, tx, org, engine.Plan(in))
		return err
	})
	if len(tasks) != 6 {
		t.Fatalf("planned %d tasks, want 6", len(tasks))
	}
	id := func(i int) string { return fmt.Sprintf("%s-%d", org, i) }
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		for i, task := range tasks[:5] {
			if err := store.MarkQueued(ctx, tx, task.ID, id(i), 0.0012); err != nil {
				return err
			}
		}
		return nil
	})

	notReady := func() (dataforseo.Answer, bool, error) { return dataforseo.Answer{}, false, nil }
	src := &scriptedEngines{script: map[string]func() (dataforseo.Answer, bool, error){
		id(0): func() (dataforseo.Answer, bool, error) {
			return dataforseo.Answer{}, false, &dataforseo.APIError{HTTPStatus: 503, Message: "service unavailable", Transient: true}
		},
		id(1): func() (dataforseo.Answer, bool, error) {
			return dataforseo.Answer{}, false, &dataforseo.APIError{HTTPStatus: 200, StatusCode: 40400, Message: "task not found"}
		},
		id(2): notReady,
		id(3): notReady,
		id(4): func() (dataforseo.Answer, bool, error) {
			return dataforseo.Answer{Present: true, Text: "Ecosa is the best mattress in Australia."}, true, nil
		},
	}}
	worker := func(giveUpAfter time.Duration, answers AnswerSource) *collectWorker {
		return &collectWorker{v: &Visibility{Pool: pool, Answers: answers, Bus: bus, Logger: slog.Default(),
			CollectAfter: time.Second, CollectEvery: 30 * time.Second, GiveUpAfter: giveUpAfter}}
	}
	collect := worker(45*time.Minute, src)
	run := func(w *collectWorker, task store.Task) error {
		return w.Work(ctx, job(jobargs.VisibilityCollect{OrgID: org, TaskID: task.ID}))
	}
	status := func(task store.Task) (state, reason string) {
		tenant(func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT status, coalesce(error, '') FROM answer_tasks WHERE id = $1`, task.ID).Scan(&state, &reason)
		})
		return state, reason
	}

	// A passing outage goes back to River, which retries with backoff; the task waits.
	if err := run(collect, tasks[0]); !dataforseo.IsTransient(err) {
		t.Errorf("a 503 returned %v, want the transient error for River to retry", err)
	}
	if s, _ := status(tasks[0]); s != "queued" {
		t.Errorf("after a 503 the task is %s, want still queued", s)
	}

	// A task DataForSEO no longer has fails visibly, and the job finishes instead of
	// retrying something that cannot succeed.
	if err := run(collect, tasks[1]); err != nil {
		t.Errorf("a permanent failure returned %v, want the job to finish", err)
	}
	if s, why := status(tasks[1]); s != "failed" || why == "" {
		t.Errorf("after task not found the task is %s (%q), want failed with the reason", s, why)
	}

	// Not ready yet: the job snoozes and tries again later; nothing waits in a loop.
	var snooze *river.JobSnoozeError
	if err := run(collect, tasks[2]); !errors.As(err, &snooze) || snooze.Duration != 30*time.Second {
		t.Errorf("a task not ready returned %v, want a 30s snooze", err)
	}
	if s, _ := status(tasks[2]); s != "queued" {
		t.Errorf("a snoozed task is %s, want still queued", s)
	}

	// Not ready for longer than Vellatry waits: given up on, and the reason says so.
	if err := run(worker(time.Nanosecond, src), tasks[3]); err != nil {
		t.Errorf("giving up returned %v, want the job to finish", err)
	}
	if s, why := status(tasks[3]); s != "failed" || why != "not ready after 1ns" {
		t.Errorf("an abandoned task is %s (%q), want failed, not ready after 1ns", s, why)
	}

	// Any job may run twice: the answer is recorded and announced once.
	for range 2 {
		if err := run(collect, tasks[4]); err != nil {
			t.Fatalf("collecting a ready answer: %v", err)
		}
	}
	var answers, announced int
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM answers`).Scan(&answers); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1`, domainevents.AnswerCollected).Scan(&announced)
	})
	if answers != 1 || announced != 1 {
		t.Errorf("after collecting one answer twice: %d answers stored, %d announced; want 1 and 1 (and none for the failures)", answers, announced)
	}

	// A task that never reached DataForSEO cannot be collected, and says why.
	if err := run(collect, tasks[5]); err != nil {
		t.Errorf("collecting an unposted task returned %v", err)
	}
	if s, why := status(tasks[5]); s != "failed" || why != "no provider task id" {
		t.Errorf("an unposted task is %s (%q), want failed, no provider task id", s, why)
	}

	// Without DataForSEO credentials the job is cancelled, not retried for ever.
	var cancel *river.JobCancelError
	if err := run(worker(45*time.Minute, nil), tasks[2]); !errors.As(err, &cancel) {
		t.Errorf("without credentials collecting returned %v, want the job cancelled", err)
	}
}

// TestPlanWhenDataForSEOFails covers posting going wrong. Nothing is retried in a loop,
// every task it could not post is marked failed with the reason, and because a failed task
// is no longer in flight, the next plan asks the question again.
func TestPlanWhenDataForSEOFails(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, domainevents.Subscriptions()...)
	plan := func(org string, answers AnswerSource) error {
		w := &planOrgWorker{v: &Visibility{Pool: pool, Answers: answers, Bus: bus, Logger: slog.Default()}}
		return w.Work(ctx, job(jobargs.VisibilityPlanOrg{OrgID: org}))
	}

	t.Run("without credentials nothing is planned", func(t *testing.T) {
		org := screeningOrg(t, pool, "plan-no-credentials")
		if err := plan(org, nil); err != nil {
			t.Fatal(err)
		}
		if states, _ := taskStates(t, pool, org); len(states) != 0 {
			t.Errorf("tasks were planned with no way to run them: %v", states)
		}
	})

	t.Run("an organisation with no brand yet is skipped", func(t *testing.T) {
		var org string
		if err := db.InSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ('half set up') RETURNING id::text`).Scan(&org)
		}); err != nil {
			t.Fatal(err)
		}
		src := &scriptedEngines{refuse: func([]dataforseo.Request) ([]dataforseo.Posted, error) { return nil, errors.New("not called") }}
		if err := plan(org, src); err != nil || len(src.batches()) != 0 {
			t.Errorf("planning an org with no brand: %v, %d posts", err, len(src.batches()))
		}
	})

	t.Run("a refused post fails its tasks, and the next plan asks again", func(t *testing.T) {
		org := screeningOrg(t, pool, "plan-refused")
		src := &scriptedEngines{refuse: func([]dataforseo.Request) ([]dataforseo.Posted, error) {
			return nil, &dataforseo.APIError{HTTPStatus: 200, StatusCode: 40501, Message: "invalid field"}
		}}
		if err := plan(org, src); err != nil {
			t.Fatalf("a refused post returned %v, want the plan to finish", err)
		}
		if states, _ := taskStates(t, pool, org); states["failed"] != 6 || len(states) != 1 {
			t.Errorf("after every post was refused: %v, want 6 failed", states)
		}
		if err := plan(org, src); err != nil {
			t.Fatal(err)
		}
		if n := len(src.batches()); n != 6 {
			t.Errorf("%d batches posted over two plans, want 3 and then 3 again", n)
		}
	})

	t.Run("a refused task fails alone", func(t *testing.T) {
		org := screeningOrg(t, pool, "plan-task-refused")
		src := &scriptedEngines{refuse: func(reqs []dataforseo.Request) ([]dataforseo.Posted, error) {
			out := make([]dataforseo.Posted, len(reqs))
			for i, r := range reqs {
				out[i] = dataforseo.Posted{Tag: r.Tag, Err: errors.New("keyword too long")}
			}
			return out, nil
		}}
		if err := plan(org, src); err != nil {
			t.Fatal(err)
		}
		if states, _ := taskStates(t, pool, org); states["failed"] != 6 || len(states) != 1 {
			t.Errorf("after every task was refused: %v, want 6 failed", states)
		}
	})
}
