package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/reports"
)

// The report bot. A reader of a published report asks a question; the answer is written
// from that report's own text and nothing else, and every figure in it has to appear in
// the report before anyone reads it.
//
// It runs here rather than in the api for the usual reason: the api never waits on a
// model. The reader's page shows the question as waiting and fills in the answer.

const reportQuestionSystem = `You answer a question about a marketing performance report, for the executive reading it.

The report below is everything you know. It is the agreed account of this period.

Rules:
- Answer only from the report. Quote its figures exactly as they appear there: same rounding, same units, same words.
- Never calculate a new figure, total, ratio, difference or percentage, even from figures the report shows.
- If the report does not answer the question, say so in one sentence and stop. That is a complete answer, and a better one than a guess.
- Do not speculate about causes the report does not state, and do not give advice the report does not support.
- Two or three short sentences. Plain Australian English, no jargon, no headings, no bullet points, no emoji.`

type reportAnswerWorker struct {
	river.WorkerDefaults[jobargs.ReportAnswer]
	r *Reports
}

func (w *reportAnswerWorker) Timeout(*river.Job[jobargs.ReportAnswer]) time.Duration {
	return 2 * time.Minute
}

func (w *reportAnswerWorker) Work(ctx context.Context, job *river.Job[jobargs.ReportAnswer]) error {
	err := w.answer(ctx, job)
	if err != nil && job.MaxAttempts > 0 && job.Attempt >= job.MaxAttempts {
		// Last attempt. Something other than the model failed, and nobody will run this
		// again: say so on the page rather than leaving the reader on "working on it"
		// for good. The error is still returned so River records the failure.
		_ = db.InTenant(ctx, w.r.Pool, job.Args.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			return reports.SaveAnswer(ctx, tx, job.Args.QuestionID, "", "failed", 0)
		})
	}
	return err
}

func (w *reportAnswerWorker) answer(ctx context.Context, job *river.Job[jobargs.ReportAnswer]) error {
	r, org := w.r, job.Args.OrgID

	var q reports.Question
	var version reports.Version
	if err := db.InTenant(ctx, r.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if q, err = reports.LoadQuestion(ctx, tx, job.Args.QuestionID); err != nil {
			return err
		}
		version, err = reports.LoadVersion(ctx, tx, q.ReportID, q.Version)
		return err
	}); err != nil {
		return err
	}
	if q.Status != "asked" {
		return nil // answered already: this is a retry of a job that got through
	}

	answer, status, dropped, err := r.writeAnswer(ctx, org, version.Snapshot, q.Question)
	if err != nil {
		// Worth another attempt: an overloaded model, a timeout. River retries, and the
		// last attempt marks the question failed so the reader is not left waiting. An
		// error that proves the account is dead reaches the halt middleware from here,
		// and while the worker is halted this job is snoozed rather than attempted, so
		// the question is still answered once someone resumes.
		return err
	}
	return db.InTenant(ctx, r.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return reports.SaveAnswer(ctx, tx, q.ID, answer, status, dropped)
	})
}

// writeAnswer asks the model, then keeps only what the report can back. A returned error
// means "try again"; everything the retry cannot fix comes back as an answer the reader
// can act on, which is to ask the team.
func (r *Reports) writeAnswer(ctx context.Context, org string, snap reports.Snapshot, question string) (answer, status string, dropped int, err error) {
	if r.Drafter == nil {
		return "Answering questions is not switched on for this account. Ask the team and they will come back to you.", "unanswerable", 0, nil
	}
	body := reports.RenderText(snap)
	if len(body) > 24_000 {
		body = body[:24_000]
	}
	resp, err := r.Drafter.Complete(ctx, gateway.Request{
		Purpose: "report_question", OrgID: org, System: reportQuestionSystem,
		Messages: []gateway.Message{{Role: "user", Content: "<report>\n" + body + "\n</report>\n\nQuestion: " + question}},
		Schema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"paragraphs"},
			"properties": map[string]any{"paragraphs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		},
	})
	switch {
	case errors.Is(err, budget.ErrExceeded):
		// This tenant has spent its month on models. Retrying cannot help, and the
		// reader is told to ask the team rather than told about our accounting.
		r.Logger.WarnContext(ctx, "report question not answered: budget", "org_id", org)
		return "", "failed", 0, nil
	case err != nil:
		return "", "", 0, err
	}
	var out struct {
		Paragraphs []string `json:"paragraphs"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil {
		// A reply we cannot read will not read better on the third attempt.
		r.Logger.WarnContext(ctx, "report answer unreadable", "org_id", org, "error", err)
		return "", "failed", 0, nil
	}
	answer, status, dropped = reports.AnswerFromReport(joinParagraphs(out.Paragraphs), snap)
	if dropped > 0 {
		// Worth knowing: it is the model reaching past the report, on a page a CMO reads.
		r.Logger.InfoContext(ctx, "report answer sentences dropped for figures the report does not show",
			"org_id", org, "dropped", dropped, "status", status)
	}
	return answer, status, dropped, nil
}

func joinParagraphs(ps []string) string {
	out := ""
	for _, p := range ps {
		if p == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += p
	}
	return out
}

// The follow-up: the reader read an answer, or read that the report cannot say, and
// asked for a person. That is a notification to the team, carrying the question.
type reportFollowUpWorker struct {
	river.WorkerDefaults[jobargs.ReportFollowUp]
	r *Reports
}

func (w *reportFollowUpWorker) Work(ctx context.Context, job *river.Job[jobargs.ReportFollowUp]) error {
	r, org := w.r, job.Args.OrgID
	return db.InTenant(ctx, r.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		q, err := reports.LoadQuestion(ctx, tx, job.Args.QuestionID)
		if err != nil {
			return err
		}
		rep, err := reports.LoadReport(ctx, tx, q.ReportID)
		if err != nil {
			return err
		}
		body := q.AskedBy + " asked, reading " + rep.Title + ":\n\n" + q.Question
		if q.Status == "answered" {
			body += "\n\nThey were shown this answer from the report:\n\n" + q.Answer
		} else {
			body += "\n\nThe report could not answer it."
		}
		_, err = r.Notify.record(ctx, tx, org, automation.Notification{
			Kind: "report_follow_up", DedupeKey: fmt.Sprintf("report_follow_up:%d", q.ID),
			Severity: "info", Title: "A question about a report needs a person",
			Body: body, Link: "/reports/" + q.ReportID, Delivery: "immediate",
			Data: map[string]any{"question_id": q.ID, "report_id": q.ReportID, "version": q.Version, "asked_by": q.AskedBy},
		})
		return err
	})
}
