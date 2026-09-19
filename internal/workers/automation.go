package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/email"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/secrets"
	"github.com/UncleSon21/vellatry/internal/slack"
)

// SlackPoster posts to an incoming webhook.
type SlackPoster interface {
	Post(ctx context.Context, url string, payload []byte) error
}

// Automation holds the watcher, notification and digest jobs' dependencies.
type Automation struct {
	Pool   *pgxpool.Pool
	Bus    *events.Bus
	Logger *slog.Logger
	Box    *secrets.Box // opens Slack webhook URLs; nil disables Slack destinations
	Slack  SlackPoster
	Email  email.Sender // nil disables email destinations
	AppURL string

	Location   *time.Location // the customers' time zone for schedules; Australia/Sydney
	DigestHour int            // the weekly digest goes out on Monday from this local hour
	Now        func() time.Time
}

func (a *Automation) defaults() {
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.Location == nil {
		a.Location = time.UTC
	}
	if a.DigestHour == 0 {
		a.DigestHour = 8
	}
	if a.Slack == nil {
		a.Slack = slack.Webhook{}
	}
}

// Register adds the automation jobs.
func (a *Automation) Register(ws *river.Workers) {
	a.defaults()
	river.AddWorker(ws, &watchEventWorker{a: a})
	river.AddWorker(ws, &watchDailyAllWorker{a: a})
	river.AddWorker(ws, &watchDailyWorker{a: a})
	river.AddWorker(ws, &notifyDeliverWorker{a: a})
	river.AddWorker(ws, &digestAllWorker{a: a})
	river.AddWorker(ws, &digestOrgWorker{a: a})
	river.AddWorker(ws, &destinationTestWorker{a: a})
}

// PeriodicJobs runs the fan-outs hourly. Each works out for itself whether today's
// watchers or this week's digest are due, and the per-tenant jobs are idempotent, so a
// restarted scheduler (River keeps its schedule in memory) can neither skip a week nor
// send one twice.
func (a *Automation) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) { return jobargs.WatchDailyAll{}, nil }, nil),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) { return jobargs.DigestAll{}, nil }, nil),
	}
}

// record stores a notification and announces it, which enqueues immediate delivery.
func (a *Automation) record(ctx context.Context, tx pgx.Tx, org string, n automation.Notification) (automation.Recorded, error) {
	r, err := automation.Record(ctx, tx, org, n)
	if err != nil || !r.New {
		return r, err
	}
	_, err = a.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.NotificationCreated, SubjectID: itoa64(r.ID), Actor: "watcher",
		Payload: domainevents.NotificationPayload{NotificationID: r.ID, Kind: n.Kind, Severity: n.Severity, Title: n.Title, Delivery: r.Delivery}})
	return r, err
}

// ---- watchers ----------------------------------------------------------------------

type watchEventWorker struct {
	river.WorkerDefaults[jobargs.WatchEvent]
	a *Automation
}

func (w *watchEventWorker) Work(ctx context.Context, job *river.Job[jobargs.WatchEvent]) error {
	org := job.Args.OrgID
	return db.InTenant(ctx, w.a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var ev events.Stored
		err := tx.QueryRow(ctx, `SELECT id, org_id::text, occurred_at, kind, coalesce(subject_id, ''), coalesce(actor, ''), payload FROM events WHERE id = $1`,
			job.Args.EventID).Scan(&ev.ID, &ev.OrgID, &ev.OccurredAt, &ev.Kind, &ev.SubjectID, &ev.Actor, &ev.Payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ns, err := automation.EvaluateEvent(ctx, tx, ev)
		if err != nil {
			return err
		}
		for _, n := range ns {
			if _, err := w.a.record(ctx, tx, org, n); err != nil {
				return err
			}
		}
		return nil
	})
}

// orgsWithBrand lists every tenant that has finished onboarding.
func orgsWithBrand(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var orgs []string
	err := db.InSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT org_id::text FROM brands`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return orgs, err
}

func insertPerOrg(ctx context.Context, orgs []string, args func(org string) river.JobArgs) error {
	if len(orgs) == 0 {
		return nil
	}
	params := make([]river.InsertManyParams, len(orgs))
	for i, o := range orgs {
		params[i] = river.InsertManyParams{Args: args(o), InsertOpts: &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}}
	}
	_, err := river.ClientFromContext[pgx.Tx](ctx).InsertMany(ctx, params)
	return err
}

type watchDailyAllWorker struct {
	river.WorkerDefaults[jobargs.WatchDailyAll]
	a *Automation
}

func (w *watchDailyAllWorker) Work(ctx context.Context, _ *river.Job[jobargs.WatchDailyAll]) error {
	orgs, err := orgsWithBrand(ctx, w.a.Pool)
	if err != nil {
		return err
	}
	day := w.a.Now().UTC().Format(time.DateOnly)
	return insertPerOrg(ctx, orgs, func(o string) river.JobArgs { return jobargs.WatchDaily{OrgID: o, Day: day} })
}

type watchDailyWorker struct {
	river.WorkerDefaults[jobargs.WatchDaily]
	a *Automation
}

func (w *watchDailyWorker) Work(ctx context.Context, job *river.Job[jobargs.WatchDaily]) error {
	day, err := time.Parse(time.DateOnly, job.Args.Day)
	if err != nil {
		return river.JobCancel(err)
	}
	return db.InTenant(ctx, w.a.Pool, job.Args.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		ns, err := automation.EvaluateDaily(ctx, tx, day)
		if err != nil {
			return err
		}
		for _, n := range ns {
			if _, err := w.a.record(ctx, tx, job.Args.OrgID, n); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- delivery ------------------------------------------------------------------------

type destination struct {
	ID, Kind, Name string
	Config         json.RawMessage
	Secret         []byte
}

type notifyDeliverWorker struct {
	river.WorkerDefaults[jobargs.NotifyDeliver]
	a *Automation
}

func (w *notifyDeliverWorker) Work(ctx context.Context, job *river.Job[jobargs.NotifyDeliver]) error {
	return w.a.deliver(ctx, job.Args.OrgID, job.Args.NotificationID)
}

var errNothingToDo = errors.New("nothing to do")

// deliver sends a notification to each of its destinations that has not accepted it
// yet. A destination that is gone is marked broken (and says so on the dashboard); a
// transient failure makes the job retry, skipping destinations already sent to.
func (a *Automation) deliver(ctx context.Context, org string, id int64) error {
	var n automation.Stored
	var dests []destination
	err := db.InTenant(ctx, a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if n, err = automation.LoadNotification(ctx, tx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNothingToDo
			}
			return err
		}
		if n.Status != "pending" {
			return errNothingToDo
		}
		rows, err := tx.Query(ctx, `
			SELECT d.id::text, d.kind, d.name, d.config, d.secret FROM destinations d
			WHERE d.status = 'active'
			  AND CASE WHEN $1 THEN d.digest WHEN cardinality($2::uuid[]) > 0 THEN d.id = ANY($2::uuid[]) ELSE true END
			  AND NOT EXISTS (SELECT 1 FROM deliveries x WHERE x.notification_id = $3 AND x.destination_id = d.id AND x.status = 'sent')
			ORDER BY d.created_at`, n.Kind == "digest", n.DestinationIDs, id)
		if err != nil {
			return err
		}
		dests, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (destination, error) {
			var d destination
			return d, r.Scan(&d.ID, &d.Kind, &d.Name, &d.Config, &d.Secret)
		})
		return err
	})
	if errors.Is(err, errNothingToDo) {
		return nil
	}
	if err != nil {
		return err
	}
	rendered, err := automation.Render(n, a.AppURL)
	if err != nil {
		return err
	}

	var transient error
	failed := 0
	for _, d := range dests {
		sendErr := a.send(ctx, org, d, n.Kind, rendered)
		permanent := sendErr != nil && (errors.Is(sendErr, slack.ErrGone) || errors.Is(sendErr, slack.ErrRejected) ||
			errors.Is(sendErr, email.ErrRejected) || errors.Is(sendErr, errNotConfigured))
		switch {
		case sendErr == nil:
		case permanent:
			failed++
			a.Logger.WarnContext(ctx, "delivery failed", "org", org, "notification", id, "destination", d.ID, "error", sendErr)
		default:
			transient = sendErr
		}
		if err := a.recordDelivery(ctx, org, id, d, sendErr); err != nil {
			return err
		}
	}
	if transient != nil {
		return transient
	}
	return db.InTenant(ctx, a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var sent int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM deliveries WHERE notification_id = $1 AND status = 'sent'`, id).Scan(&sent); err != nil {
			return err
		}
		status := "in_app"
		switch {
		case sent > 0 && failed > 0:
			status = "partial"
		case sent > 0:
			status = "sent"
		case failed > 0:
			status = "failed"
		}
		_, err := tx.Exec(ctx, `UPDATE notifications SET status = $2, sent_at = CASE WHEN $2 IN ('sent', 'partial') THEN now() END WHERE id = $1`, id, status)
		return err
	})
}

var errNotConfigured = errors.New("not configured on this server")

func (a *Automation) send(ctx context.Context, org string, d destination, kind string, r automation.Rendered) error {
	switch d.Kind {
	case "slack":
		if a.Box == nil {
			return fmt.Errorf("slack: %w", errNotConfigured)
		}
		url, err := a.Box.Open(org, d.Secret)
		if err != nil {
			return fmt.Errorf("slack: open webhook: %w", err)
		}
		return a.Slack.Post(ctx, string(url), r.Slack)
	case "email":
		if a.Email == nil {
			return fmt.Errorf("email: %w", errNotConfigured)
		}
		var cfg struct {
			To []string `json:"to"`
		}
		if err := json.Unmarshal(d.Config, &cfg); err != nil || len(cfg.To) == 0 {
			return fmt.Errorf("%w: no recipients", email.ErrRejected)
		}
		subject := r.Subject
		if kind != "digest" {
			subject = "Vellatry: " + subject
		}
		return a.Email.Send(ctx, email.Message{To: cfg.To, Subject: subject, Text: r.Text, HTML: r.HTML, Tag: kind})
	}
	return fmt.Errorf("%w: unknown destination kind %q", email.ErrRejected, d.Kind)
}

// recordDelivery stores the attempt and, when Slack says the webhook is gone, marks the
// destination broken so the dashboard shows it with a fix.
func (a *Automation) recordDelivery(ctx context.Context, org string, id int64, d destination, sendErr error) error {
	return db.InTenant(ctx, a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		status, detail := "sent", ""
		if sendErr != nil {
			status, detail = "failed", sendErr.Error()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deliveries (notification_id, destination_id, org_id, status, error) VALUES ($1, $2, $3, $4, nullif($5, ''))
			ON CONFLICT (notification_id, destination_id) DO UPDATE SET status = EXCLUDED.status, error = EXCLUDED.error, attempted_at = now()`,
			id, d.ID, org, status, detail); err != nil {
			return err
		}
		if sendErr == nil || !errors.Is(sendErr, slack.ErrGone) {
			return nil
		}
		return a.markDestinationBroken(ctx, tx, org, d, "Slack no longer accepts messages for "+d.Name+". Add the channel again with a new webhook.")
	})
}

func (a *Automation) markDestinationBroken(ctx context.Context, tx pgx.Tx, org string, d destination, detail string) error {
	tag, err := tx.Exec(ctx, `UPDATE destinations SET status = 'broken', status_detail = $2, updated_at = now() WHERE id = $1 AND status <> 'broken'`, d.ID, detail)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = a.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.ConnectionBroken, SubjectID: d.ID, Actor: "notify",
		Payload: domainevents.ConnectionPayload{Kind: d.Kind, Detail: detail}})
	return err
}

// ---- digest --------------------------------------------------------------------------

type digestAllWorker struct {
	river.WorkerDefaults[jobargs.DigestAll]
	a *Automation
}

// DigestWeek returns the Monday (local date) whose digest is due at now, or "" before
// the digest hour on a Monday, when last week's digest is still the latest due.
func DigestWeek(now time.Time, loc *time.Location, hour int) string {
	local := now.In(loc)
	daysSinceMonday := (int(local.Weekday()) + 6) % 7
	monday := time.Date(local.Year(), local.Month(), local.Day()-daysSinceMonday, 0, 0, 0, 0, loc)
	if daysSinceMonday == 0 && local.Hour() < hour {
		monday = monday.AddDate(0, 0, -7)
	}
	return monday.Format(time.DateOnly)
}

func (w *digestAllWorker) Work(ctx context.Context, _ *river.Job[jobargs.DigestAll]) error {
	orgs, err := orgsWithBrand(ctx, w.a.Pool)
	if err != nil {
		return err
	}
	week := DigestWeek(w.a.Now(), w.a.Location, w.a.DigestHour)
	return insertPerOrg(ctx, orgs, func(o string) river.JobArgs { return jobargs.DigestOrg{OrgID: o, Week: week} })
}

type digestOrgWorker struct {
	river.WorkerDefaults[jobargs.DigestOrg]
	a *Automation
}

func (w *digestOrgWorker) Work(ctx context.Context, job *river.Job[jobargs.DigestOrg]) error {
	monday, err := time.Parse(time.DateOnly, job.Args.Week)
	if err != nil {
		return river.JobCancel(err)
	}
	from, to := monday.AddDate(0, 0, -7), monday.AddDate(0, 0, -1)
	org := job.Args.OrgID
	return db.InTenant(ctx, w.a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notifications WHERE dedupe_key = $1)`, "digest:"+job.Args.Week).Scan(&exists); err != nil || exists {
			return err // already built; its delivery job handles sending
		}
		d, included, err := automation.BuildDigest(ctx, tx, from, to)
		if err != nil || d.Empty() {
			return err
		}
		r, err := w.a.record(ctx, tx, org, automation.Notification{
			Kind: "digest", DedupeKey: "digest:" + job.Args.Week, Title: fmt.Sprintf("Weekly digest, %s to %s", d.From, d.To),
			Data: d, Delivery: "immediate",
		})
		if err != nil {
			return err
		}
		if len(included) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE notifications SET status = 'digested' WHERE id = ANY($1)`, included); err != nil {
				return err
			}
		}
		_, err = w.a.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.DigestSent, SubjectID: itoa64(r.ID), Actor: "digest",
			Payload: map[string]any{"week": job.Args.Week, "alerts": len(included)}})
		return err
	})
}

// ---- destinations --------------------------------------------------------------------

type destinationTestWorker struct {
	river.WorkerDefaults[jobargs.DestinationTest]
	a *Automation
}

// Work sends a first message so a wrong webhook or address shows up now, not when the
// first real alert is lost.
func (w *destinationTestWorker) Work(ctx context.Context, job *river.Job[jobargs.DestinationTest]) error {
	org := job.Args.OrgID
	var d destination
	var brandName string
	err := db.InTenant(ctx, w.a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT d.id::text, d.kind, d.name, d.config, d.secret, coalesce((SELECT name FROM brands ORDER BY created_at LIMIT 1), '')
			FROM destinations d WHERE d.id = $1 AND d.status = 'active'`, job.Args.DestinationID).
			Scan(&d.ID, &d.Kind, &d.Name, &d.Config, &d.Secret, &brandName)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNothingToDo
		}
		return err
	})
	if errors.Is(err, errNothingToDo) {
		return nil
	}
	if err != nil {
		return err
	}
	link := "/automations"
	r, err := automation.Render(automation.Stored{
		Kind: "destination_test", Title: "Vellatry is connected", Link: &link, Occurrences: 1,
		Body: fmt.Sprintf("Alerts and the weekly digest for %s will arrive here. Choose what is sent under Automations.", brandName),
	}, w.a.AppURL)
	if err != nil {
		return err
	}
	sendErr := w.a.send(ctx, org, d, "destination_test", r)
	if sendErr == nil {
		return nil
	}
	permanent := errors.Is(sendErr, slack.ErrGone) || errors.Is(sendErr, slack.ErrRejected) || errors.Is(sendErr, email.ErrRejected) || errors.Is(sendErr, errNotConfigured)
	if !permanent {
		return sendErr
	}
	return db.InTenant(ctx, w.a.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		detail := "The test message could not be delivered: " + sendErr.Error()
		if errors.Is(sendErr, slack.ErrGone) {
			detail = "Slack did not accept the webhook. Check it was copied in full and the channel still exists."
		}
		return w.a.markDestinationBroken(ctx, tx, org, d, detail)
	})
}
