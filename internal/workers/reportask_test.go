package workers

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/reports"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// answerBot replies to the report bot's purpose only, with whatever it is told to say.
type answerBot struct {
	calls   int
	reply   string
	err     error
	sawBody string
}

func (a *answerBot) Complete(_ context.Context, req gateway.Request) (gateway.Response, error) {
	a.calls++
	if req.Purpose != "report_question" {
		return gateway.Response{}, nil
	}
	a.sawBody = req.Messages[0].Content
	return gateway.Response{Text: a.reply}, a.err
}

// The CMO asks a question of a published report. The answer comes from that version's
// snapshot, every figure in it is one the report shows, and a question the report cannot
// answer says so rather than guessing.
func TestReportQuestionAnswered(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "askbot")
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, domainevents.Subscriptions()...)
	bot := &answerBot{}
	auto := &Automation{Pool: pool, Bus: bus, Logger: slog.Default(), Slack: &fakeSlack{}}
	auto.Register(river.NewWorkers())
	r := &Reports{Pool: pool, Bus: bus, Logger: slog.Default(), Notify: auto, Drafter: bot, HubURL: "https://reports.test"}
	r.Register(river.NewWorkers())
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	// A published report with real figures in it.
	snap := reports.Snapshot{Version: reports.SnapshotVersion, Org: "Koala Pty Ltd", Brand: "Koala",
		Period:   reports.Period{Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Label: "August 2026"},
		Previous: reports.Period{Label: "July 2026"}, Order: []string{reports.SecVisibility},
		Visibility: &reports.VisibilitySection{Answers: 300, Visibility: reports.Pair{Current: ptrf(41.0), Previous: ptrf(35.5)}}}
	var reportID string
	var version int
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var seriesID string
		if err := tx.QueryRow(ctx, `INSERT INTO report_series (org_id, name, period) VALUES ($1, 'Monthly performance', 'month') RETURNING id::text`, org).Scan(&seriesID); err != nil {
			return err
		}
		sr, err := reports.LoadSeries(ctx, tx, seriesID)
		if err != nil {
			return err
		}
		if reportID, _, err = reports.SaveDraft(ctx, tx, org, sr, snap, "", false); err != nil {
			return err
		}
		version, err = reports.Publish(ctx, tx, org, reportID, "lead")
		return err
	})

	const cmo = "cmo@koala.example"
	ask := func(email, question string) int64 {
		t.Helper()
		var id int64
		tenant(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			id, err = reports.Ask(ctx, tx, org, reportID, version, email, question)
			return err
		})
		return id
	}
	load := func(id int64) reports.Question {
		t.Helper()
		var q reports.Question
		tenant(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			q, err = reports.LoadQuestion(ctx, tx, id)
			return err
		})
		return q
	}
	answer := &reportAnswerWorker{r: r}

	// One grounded sentence, one with a figure the report does not show. The second goes.
	bot.reply = `{"paragraphs": ["Visibility was 41.0% in August 2026, up 5.5 pts on July.", "Most of that came from a 20% lift in campaign spend."]}`
	id := ask(cmo, "Why did visibility move?")
	if err := answer.Work(ctx, job(jobargs.ReportAnswer{OrgID: org, QuestionID: id})); err != nil {
		t.Fatal(err)
	}
	q := load(id)
	if q.Status != "answered" || q.Dropped != 1 || q.Answer != "Visibility was 41.0% in August 2026, up 5.5 pts on July." {
		t.Errorf("answer = %q, %s, %d dropped", q.Answer, q.Status, q.Dropped)
	}
	if q.AnsweredAt == nil {
		t.Error("an answered question has no answered_at")
	}
	// The model is handed the report and nothing else.
	if !strings.Contains(bot.sawBody, "## AI visibility") || !strings.Contains(bot.sawBody, "Why did visibility move?") {
		t.Errorf("the prompt did not carry the report and the question: %q", bot.sawBody)
	}

	// Nothing the report backs: it says so, in the words the reader sees, and does not guess.
	bot.reply = `{"paragraphs": ["Churn was 4% this quarter."]}`
	id = ask(cmo, "What is our churn?")
	if err := answer.Work(ctx, job(jobargs.ReportAnswer{OrgID: org, QuestionID: id})); err != nil {
		t.Fatal(err)
	}
	if q := load(id); q.Status != "unanswerable" || q.Answer != reports.WhenTheReportCannotSay {
		t.Errorf("unanswerable question = %q, %s", q.Answer, q.Status)
	}

	// Running the same job twice does not re-ask the model or overwrite the answer.
	before := bot.calls
	if err := answer.Work(ctx, job(jobargs.ReportAnswer{OrgID: org, QuestionID: id})); err != nil {
		t.Fatal(err)
	}
	if bot.calls != before {
		t.Error("a repeated job asked the model again")
	}

	// A reply that cannot be read is not worth three attempts: the question says so,
	// and the reader can send it to the team.
	bot.reply = "I'd rather not answer that in JSON."
	id = ask(cmo, "What about search?")
	if err := answer.Work(ctx, job(jobargs.ReportAnswer{OrgID: org, QuestionID: id})); err != nil {
		t.Errorf("an unreadable reply should not be retried: %v", err)
	}
	if q := load(id); q.Status != "failed" {
		t.Errorf("after an unreadable reply the question is %s, want failed", q.Status)
	}

	// An overloaded model is worth another attempt, and the reader keeps waiting.
	bot.err = errors.New("529 overloaded")
	bot.reply = ""
	id = ask(cmo, "And by city?")
	if err := answer.Work(ctx, job(jobargs.ReportAnswer{OrgID: org, QuestionID: id})); err == nil {
		t.Error("an overloaded model should fail the job so River retries it")
	}
	if q := load(id); q.Status != "asked" {
		t.Errorf("a question being retried is %s, want asked", q.Status)
	}
	bot.err = nil

	// A reader with questions waiting cannot queue more paid work without limit.
	for i := 0; i < reports.MaxOpenQuestions-1; i++ {
		ask(cmo, "Another one")
	}
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := reports.Ask(ctx, tx, org, reportID, version, cmo, "And one more")
		return err
	}); err == nil {
		t.Error("a fourth waiting question was accepted")
	}

	// The follow-up: the team gets a notification carrying the question. A different
	// reader, because the first one's three are still waiting.
	id = ask("cfo@koala.example", "Can someone explain the drop?")
	tenant(func(ctx context.Context, tx pgx.Tx) error { return reports.MarkFollowUp(ctx, tx, id) })
	if err := (&reportFollowUpWorker{r: r}).Work(ctx, job(jobargs.ReportFollowUp{OrgID: org, QuestionID: id})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var title, body string
		if err := tx.QueryRow(ctx, `SELECT title, body FROM notifications WHERE kind = 'report_follow_up'`).Scan(&title, &body); err != nil {
			return err
		}
		if !strings.Contains(body, "Can someone explain the drop?") || !strings.Contains(body, "cfo@koala.example") {
			t.Errorf("the team notice does not carry the question: %q", body)
		}
		return nil
	})
}

func ptrf(v float64) *float64 { return &v }
