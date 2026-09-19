// Package workers holds the worker role's jobs. Every job is safe to run twice.
package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/brand"
	"github.com/UncleSon21/vellatry/internal/dataforseo"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/metering"
	"github.com/UncleSon21/vellatry/internal/visibility/engine"
	"github.com/UncleSon21/vellatry/internal/visibility/judge"
	"github.com/UncleSon21/vellatry/internal/visibility/store"
)

// AnswerSource is the part of the DataForSEO client the Visibility jobs use.
type AnswerSource interface {
	Live(ctx context.Context, r dataforseo.Request) (dataforseo.Answer, error)
	PostTasks(ctx context.Context, reqs []dataforseo.Request) ([]dataforseo.Posted, error)
	GetTask(ctx context.Context, e dataforseo.Engine, taskID string) (dataforseo.Answer, bool, error)
}

// Visibility holds the Visibility engine's job dependencies.
type Visibility struct {
	Pool      *pgxpool.Pool
	Answers   AnswerSource
	Judge     judge.Completer // nil disables judging
	JudgeName string          // model + rubric version, stored with each verdict
	Bus       *events.Bus
	Logger    *slog.Logger
	// CollectAfter is how long to wait before the first collection attempt, and
	// CollectEvery the snooze between attempts while a task is still queued.
	CollectAfter, CollectEvery, GiveUpAfter time.Duration
}

func (v *Visibility) defaults() {
	if v.CollectAfter == 0 {
		v.CollectAfter = 90 * time.Second
	}
	if v.CollectEvery == 0 {
		v.CollectEvery = 30 * time.Second
	}
	if v.GiveUpAfter == 0 {
		v.GiveUpAfter = 45 * time.Minute
	}
}

// Register adds the Visibility jobs to workers.
func (v *Visibility) Register(workers *river.Workers) {
	v.defaults()
	river.AddWorker(workers, &planAllWorker{v: v})
	river.AddWorker(workers, &planOrgWorker{v: v})
	river.AddWorker(workers, &collectWorker{v: v})
	river.AddWorker(workers, &judgeWorker{v: v})
	river.AddWorker(workers, &checkNowWorker{v: v})
}

// PeriodicJobs schedules planning for every tenant.
func (v *Visibility) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return jobargs.VisibilityPlanAll{}, nil
		}, &river.PeriodicJobOpts{RunOnStart: true}),
	}
}

// ---- plan ------------------------------------------------------------------------

type planAllWorker struct {
	river.WorkerDefaults[jobargs.VisibilityPlanAll]
	v *Visibility
}

func (w *planAllWorker) Work(ctx context.Context, _ *river.Job[jobargs.VisibilityPlanAll]) error {
	var orgs []string
	err := db.InSystem(ctx, w.v.Pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT org_id::text FROM brands`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil || len(orgs) == 0 {
		return err
	}
	params := make([]river.InsertManyParams, len(orgs))
	for i, o := range orgs {
		params[i] = river.InsertManyParams{Args: jobargs.VisibilityPlanOrg{OrgID: o}, InsertOpts: &river.InsertOpts{
			UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 10 * time.Minute},
		}}
	}
	_, err = river.ClientFromContext[pgx.Tx](ctx).InsertMany(ctx, params)
	return err
}

type planOrgWorker struct {
	river.WorkerDefaults[jobargs.VisibilityPlanOrg]
	v *Visibility
}

func (w *planOrgWorker) Work(ctx context.Context, job *river.Job[jobargs.VisibilityPlanOrg]) error {
	if w.v.Answers == nil {
		return nil // no DataForSEO credentials: plan nothing rather than tasks that cannot run
	}
	org := job.Args.OrgID
	var settings store.Settings
	var tasks []store.Task
	err := db.InTenant(ctx, w.v.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, err := brand.Load(ctx, tx); err != nil {
			return err
		}
		var err error
		if settings, err = store.LoadSettings(ctx, tx); err != nil {
			return err
		}
		in, err := store.LoadPlanInput(ctx, tx, settings)
		if err != nil {
			return err
		}
		tasks, err = store.CreateTasks(ctx, tx, org, engine.Plan(in))
		return err
	})
	if errors.Is(err, brand.ErrNoBrand) {
		return nil
	}
	if err != nil || len(tasks) == 0 {
		return err
	}
	w.v.Logger.InfoContext(ctx, "visibility plan", "org_id", org, "tasks", len(tasks))
	return w.v.post(ctx, org, settings, tasks)
}

// post sends tasks to the standard queue in batches per engine and schedules a
// collector for each. A batch that cannot be posted is marked failed; the cells still
// need their answers, so the next plan requests them again.
func (v *Visibility) post(ctx context.Context, org string, s store.Settings, tasks []store.Task) error {
	byEngine := map[string][]store.Task{}
	for _, t := range tasks {
		byEngine[t.Engine] = append(byEngine[t.Engine], t)
	}
	for eng, list := range byEngine {
		for i := 0; i < len(list); i += 100 {
			batch := list[i:min(i+100, len(list))]
			reqs := make([]dataforseo.Request, len(batch))
			for k, t := range batch {
				reqs[k] = request(t, eng, s)
			}
			posted, err := v.Answers.PostTasks(ctx, reqs)
			if err != nil {
				v.Logger.WarnContext(ctx, "post failed", "org_id", org, "engine", eng, "tasks", len(batch), "error", err)
				if ferr := v.failAll(ctx, org, batch, err.Error()); ferr != nil {
					return ferr
				}
				if errors.Is(err, budget.ErrExceeded) {
					return nil // our account's daily cap: stop posting, nothing to retry today
				}
				continue
			}
			if err := v.queue(ctx, org, eng, batch, posted); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *Visibility) queue(ctx context.Context, org, eng string, batch []store.Task, posted []dataforseo.Posted) error {
	return db.InTenant(ctx, v.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var collect []river.InsertManyParams
		for k, p := range posted {
			t := batch[k]
			if p.Err != nil {
				if err := store.MarkFailed(ctx, tx, t.ID, p.Err.Error()); err != nil {
					return err
				}
				continue
			}
			if err := store.MarkQueued(ctx, tx, t.ID, p.TaskID, p.Cost); err != nil {
				return err
			}
			if err := metering.Record(ctx, tx, org, metering.Usage{
				Provider: "dataforseo", Purpose: "visibility." + string(t.Purpose), Units: 1, CostUSD: p.Cost, Ref: p.TaskID,
			}); err != nil {
				return err
			}
			collect = append(collect, river.InsertManyParams{
				Args:       jobargs.VisibilityCollect{OrgID: org, TaskID: t.ID},
				InsertOpts: &river.InsertOpts{ScheduledAt: time.Now().Add(v.CollectAfter)},
			})
		}
		if len(collect) == 0 {
			return nil
		}
		return db.AsOwner(ctx, tx, func() error {
			_, err := river.ClientFromContext[pgx.Tx](ctx).InsertManyTx(ctx, tx, collect)
			return err
		})
	})
}

func (v *Visibility) failAll(ctx context.Context, org string, batch []store.Task, reason string) error {
	return db.InTenant(ctx, v.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		for _, t := range batch {
			if err := store.MarkFailed(ctx, tx, t.ID, reason); err != nil {
				return err
			}
		}
		return nil
	})
}

func request(t store.Task, eng string, s store.Settings) dataforseo.Request {
	return dataforseo.Request{
		Engine: dataforseo.Engine(eng), Keyword: t.PromptText, Tag: fmt.Sprint(t.ID),
		LocationCode: s.LocationCode, LanguageCode: s.LanguageCode,
		// force_web_search stays off: consumer ChatGPT decides per question, as for real users.
	}
}

// ---- collect -----------------------------------------------------------------------

type collectWorker struct {
	river.WorkerDefaults[jobargs.VisibilityCollect]
	v *Visibility
}

func (w *collectWorker) Work(ctx context.Context, job *river.Job[jobargs.VisibilityCollect]) error {
	if w.v.Answers == nil {
		return river.JobCancel(errors.New("visibility engine disabled: no DataForSEO credentials"))
	}
	org := job.Args.OrgID
	var t store.Task
	err := db.InTenant(ctx, w.v.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		t, err = store.GetTask(ctx, tx, job.Args.TaskID)
		return err
	})
	if err != nil {
		return err
	}
	if t.Status == "done" || t.Status == "failed" {
		return nil
	}
	if t.ProviderTaskID == "" {
		return w.v.failAll(ctx, org, []store.Task{t}, "no provider task id")
	}
	ans, ready, err := w.v.Answers.GetTask(ctx, dataforseo.Engine(t.Engine), t.ProviderTaskID)
	switch {
	case err != nil && dataforseo.IsTransient(err):
		return err // River retries with backoff
	case err != nil:
		return w.v.failAll(ctx, org, []store.Task{t}, err.Error())
	case !ready && time.Since(t.CreatedAt) > w.v.GiveUpAfter:
		return w.v.failAll(ctx, org, []store.Task{t}, "not ready after "+w.v.GiveUpAfter.String())
	case !ready:
		return river.JobSnooze(w.v.CollectEvery)
	}
	return w.v.record(ctx, t, ans)
}

// record stores an answer and announces what changed.
func (v *Visibility) record(ctx context.Context, t store.Task, a dataforseo.Answer) error {
	return db.InTenant(ctx, v.Pool, t.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		b, comps, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		be, ce := brand.Entities(b, comps)
		out, err := store.RecordAnswer(ctx, tx, t, observed(a), be, ce)
		if err != nil || !out.New {
			return err
		}
		if _, err := v.Bus.Emit(ctx, tx, t.OrgID, events.Event{
			Kind: domainevents.AnswerCollected, SubjectID: fmt.Sprint(out.AnswerID), Actor: "visibility",
			Payload: domainevents.AnswerCollectedPayload{AnswerID: out.AnswerID, PromptID: t.PromptID, Engine: t.Engine, Mentioned: out.Mentioned, Band: string(out.Band)},
		}); err != nil {
			return err
		}
		return v.announce(ctx, tx, t.OrgID, out.Opened, out.Resolved)
	})
}

func (v *Visibility) announce(ctx context.Context, tx pgx.Tx, org string, opened, resolved []store.Change) error {
	for _, c := range opened {
		if _, err := v.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.BlindspotOpened, SubjectID: c.PromptID, Actor: "visibility", Payload: c}); err != nil {
			return err
		}
	}
	for _, c := range resolved {
		if _, err := v.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.BlindspotResolved, SubjectID: c.PromptID, Actor: "visibility", Payload: c}); err != nil {
			return err
		}
	}
	return nil
}

func observed(a dataforseo.Answer) store.Observed {
	o := store.Observed{Present: a.Present, Text: a.Text, FanOut: a.FanOut, BrandEntities: a.BrandEntities, Model: a.Model}
	for _, s := range a.Sources {
		o.Sources = append(o.Sources, store.Source{URL: s.URL, Title: s.Title, Domain: s.Domain})
	}
	return o
}

// ---- judge -------------------------------------------------------------------------

type judgeWorker struct {
	river.WorkerDefaults[jobargs.VisibilityJudge]
	v *Visibility
}

func (w *judgeWorker) Work(ctx context.Context, job *river.Job[jobargs.VisibilityJudge]) error {
	if w.v.Judge == nil {
		return nil
	}
	org := job.Args.OrgID
	var a store.AnswerForJudging
	var in judge.Input
	var enabled bool
	err := db.InTenant(ctx, w.v.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		s, err := store.LoadSettings(ctx, tx)
		if err != nil {
			return err
		}
		enabled = s.JudgeEnabled
		b, comps, err := brand.Load(ctx, tx)
		if err != nil {
			return err
		}
		if a, err = store.LoadAnswerForJudging(ctx, tx, job.Args.AnswerID, w.v.JudgeName); err != nil {
			return err
		}
		in = judge.Input{Brand: b.Name, Aliases: b.Aliases, Differentiators: b.Differentiators, Question: a.Question, Answer: a.Text}
		for _, c := range comps {
			in.Competitors = append(in.Competitors, c.Name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !enabled || a.Judged || !a.Mentioned {
		return nil
	}

	r, _, err := judge.Judge(ctx, w.v.Judge, org, in)
	switch {
	case errors.Is(err, budget.ErrExceeded):
		return river.JobSnooze(untilNextUTCDay()) // the tenant's LLM budget for today is spent
	case errors.Is(err, judge.ErrTooLong), errors.Is(err, judge.ErrInvalid),
		errors.Is(err, gateway.ErrRefused), errors.Is(err, gateway.ErrTruncated), errors.Is(err, gateway.ErrPerItemRefused):
		w.v.Logger.WarnContext(ctx, "answer left unjudged", "org_id", org, "answer_id", a.AnswerID, "reason", err)
		return nil // deterministic: retrying would pay for the same failure
	case err != nil:
		return err
	}

	return db.InTenant(ctx, w.v.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		out, err := store.RecordJudgment(ctx, tx, org, a, w.v.JudgeName, r, len(in.Differentiators))
		if err != nil || !out.New {
			return err
		}
		if _, err := w.v.Bus.Emit(ctx, tx, org, events.Event{
			Kind: domainevents.AnswerJudged, SubjectID: fmt.Sprint(a.AnswerID), Actor: "judge",
			Payload: map[string]any{"answer_id": a.AnswerID, "sentiment": judge.SentimentScore(r), "role": r.Role.Value},
		}); err != nil {
			return err
		}
		return w.v.announce(ctx, tx, org, out.Opened, out.Resolved)
	})
}

func untilNextUTCDay() time.Duration {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 5, 0, 0, time.UTC)
	return next.Sub(now)
}

// ---- check now ---------------------------------------------------------------------

type checkNowWorker struct {
	river.WorkerDefaults[jobargs.VisibilityCheckNow]
	v *Visibility
}

func (w *checkNowWorker) Work(ctx context.Context, job *river.Job[jobargs.VisibilityCheckNow]) error {
	if w.v.Answers == nil {
		return river.JobCancel(errors.New("visibility engine disabled: no DataForSEO credentials"))
	}
	org := job.Args.OrgID
	var settings store.Settings
	var tasks []store.Task
	err := db.InTenant(ctx, w.v.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if settings, err = store.LoadSettings(ctx, tx); err != nil {
			return err
		}
		in, err := store.LoadPlanInput(ctx, tx, settings)
		if err != nil {
			return err
		}
		if in.Budget < len(settings.Engines) {
			// "Check now" spends the same daily budget as everything else; it never overdraws it.
			_, err := w.v.Bus.Emit(ctx, tx, org, events.Event{
				Kind: domainevents.CheckSkipped, SubjectID: job.Args.PromptID, Actor: "visibility",
				Payload: map[string]any{"reason": "daily answer budget reached", "remaining": in.Budget},
			})
			return err
		}
		var round int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(round), 1) FROM cells WHERE prompt_id = $1`, job.Args.PromptID).Scan(&round); err != nil {
			return err
		}
		specs := make([]engine.TaskSpec, len(settings.Engines))
		for i, e := range settings.Engines {
			specs[i] = engine.TaskSpec{PromptID: job.Args.PromptID, Engine: e, Purpose: engine.PurposeCheck, Round: round}
		}
		tasks, err = store.CreateTasks(ctx, tx, org, specs)
		return err
	})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		a, err := w.v.Answers.Live(ctx, request(t, t.Engine, settings))
		if err != nil {
			if ferr := w.v.failAll(ctx, org, []store.Task{t}, err.Error()); ferr != nil {
				return ferr
			}
			continue
		}
		if err := db.InTenant(ctx, w.v.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			if err := store.MarkQueued(ctx, tx, t.ID, a.TaskID, a.Cost); err != nil {
				return err
			}
			return metering.Record(ctx, tx, org, metering.Usage{Provider: "dataforseo", Purpose: "visibility.check", Units: 1, CostUSD: a.Cost, Ref: a.TaskID})
		}); err != nil {
			return err
		}
		if err := w.v.record(ctx, t, a); err != nil {
			return err
		}
	}
	return nil
}
