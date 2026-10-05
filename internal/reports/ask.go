package reports

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The report bot: the CMO asks a question about a report they are reading, and the
// answer comes from that version's frozen snapshot and nothing else. Not live data, not
// a different report. The report is the agreed account of the period, and an answer that
// quietly knew more than it would be a different claim than the one that was published.
//
// A model may word the answer. Every figure in it must already appear in the report, and
// any sentence carrying one that does not is dropped before anyone reads it: the same
// check the published summary passes (KeepVerified).

// MaxQuestion is as long as a question may be. Longer is a brief, not a question.
const MaxQuestion = 300

// MaxOpenQuestions is how many unanswered questions one reader may have at a time. It
// stops a stuck or abusive page from queueing paid work without limit.
const MaxOpenQuestions = 3

// WhenTheReportCannotSay is the answer when nothing survives the figure check, or the
// model found nothing to answer with. It says whose limit it is.
const WhenTheReportCannotSay = "This report does not cover that. Ask the team to look into it and they will come back to you."

// Question is one asked of a report version.
type Question struct {
	ID         int64
	ReportID   string
	Version    int
	AskedBy    string
	Question   string
	Answer     string
	Status     string
	Dropped    int
	FollowUp   *time.Time
	AskedAt    time.Time
	AnsweredAt *time.Time
}

// What Ask refuses. They are sentinels because the caller has to tell them apart from a
// database failure: one is a sentence for the reader, the other is a bug.
var (
	ErrQuestionsWaiting = errors.New("reports: too many questions waiting")
	ErrEmptyQuestion    = errors.New("reports: the question is empty")
	ErrQuestionTooLong  = errors.New("reports: the question is too long")
)

// Ask records a question against a published version. tx must be scoped to the org.
func Ask(ctx context.Context, tx pgx.Tx, org, reportID string, version int, email, question string) (int64, error) {
	question = strings.Join(strings.Fields(question), " ")
	if question == "" {
		return 0, ErrEmptyQuestion
	}
	if utf8.RuneCountInString(question) > MaxQuestion {
		return 0, ErrQuestionTooLong
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM report_questions WHERE asked_by = $1 AND status = 'asked'`, email).Scan(&open); err != nil {
		return 0, err
	}
	if open >= MaxOpenQuestions {
		return 0, ErrQuestionsWaiting
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO report_questions (org_id, report_id, version, asked_by, question)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, org, reportID, version, email, question).Scan(&id)
	return id, err
}

// LoadQuestion returns one question. tx must be scoped to the org.
func LoadQuestion(ctx context.Context, tx pgx.Tx, id int64) (Question, error) {
	var q Question
	err := tx.QueryRow(ctx, `
		SELECT id, report_id::text, version, asked_by, question, coalesce(answer, ''), status, dropped, follow_up, asked_at, answered_at
		FROM report_questions WHERE id = $1`, id).
		Scan(&q.ID, &q.ReportID, &q.Version, &q.AskedBy, &q.Question, &q.Answer, &q.Status, &q.Dropped, &q.FollowUp, &q.AskedAt, &q.AnsweredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, ErrNotFound
	}
	return q, err
}

// QuestionsBy returns one reader's questions of one version, oldest first. tx must be
// scoped to the org.
//
// A reader sees their own questions and not their colleagues'. Everyone here works at
// the same company, so this is not a confidentiality rule; it is that a question is a
// person thinking aloud, and people ask fewer of them in front of an audience.
func QuestionsBy(ctx context.Context, tx pgx.Tx, reportID string, version int, email string) ([]Question, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, report_id::text, version, asked_by, question, coalesce(answer, ''), status, dropped, follow_up, asked_at, answered_at
		FROM report_questions WHERE report_id = $1 AND version = $2 AND asked_by = $3 ORDER BY asked_at`, reportID, version, email)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanQuestion)
}

func scanQuestion(r pgx.CollectableRow) (Question, error) {
	var q Question
	err := r.Scan(&q.ID, &q.ReportID, &q.Version, &q.AskedBy, &q.Question, &q.Answer, &q.Status, &q.Dropped, &q.FollowUp, &q.AskedAt, &q.AnsweredAt)
	return q, err
}

// AllQuestions returns every question asked of a report, newest first, whoever asked and
// whichever version. It is for the team who wrote it: what a reader had to ask is the
// clearest thing a report can say about what it left out.
func AllQuestions(ctx context.Context, tx pgx.Tx, reportID string, limit int) ([]Question, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, report_id::text, version, asked_by, question, coalesce(answer, ''), status, dropped, follow_up, asked_at, answered_at
		FROM report_questions WHERE report_id = $1 ORDER BY asked_at DESC LIMIT $2`, reportID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanQuestion)
}

// SaveAnswer stores what the reader will see. tx must be scoped to the org.
func SaveAnswer(ctx context.Context, tx pgx.Tx, id int64, answer, status string, dropped int) error {
	_, err := tx.Exec(ctx, `UPDATE report_questions SET answer = $2, status = $3, dropped = $4, answered_at = now() WHERE id = $1`,
		id, answer, status, dropped)
	return err
}

// MarkFollowUp records that the reader asked the team to look into it. tx must be
// scoped to the org.
func MarkFollowUp(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `UPDATE report_questions SET follow_up = now() WHERE id = $1 AND follow_up IS NULL`, id)
	return err
}

// AnswerFromReport keeps only what the report can back: paragraphs whose every figure
// the report already shows. It returns the answer to store, its status, and how many
// paragraphs were dropped.
//
// Dropping rather than repairing is the point. A sentence with a figure the report does
// not show is a claim nobody checked, and the reader is a CMO who will repeat it.
func AnswerFromReport(written string, s Snapshot) (answer, status string, dropped int) {
	kept, dropped := KeepVerified(Paragraphs(written), Figures(s))
	if len(kept) == 0 {
		return WhenTheReportCannotSay, "unanswerable", dropped
	}
	return strings.Join(kept, "\n\n"), "answered", dropped
}
