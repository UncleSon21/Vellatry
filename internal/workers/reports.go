package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/email"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/pdf"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/reports"
)

// Completer is the part of the LLM gateway the summary drafter needs.
type Completer interface {
	Complete(ctx context.Context, req gateway.Request) (gateway.Response, error)
}

// PDFRenderer turns report HTML into a PDF.
type PDFRenderer interface {
	HTMLToPDF(ctx context.Context, html []byte) ([]byte, error)
}

// Reports holds the report jobs' dependencies.
type Reports struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Logger   *slog.Logger
	Notify   *Automation  // records team notifications
	Email    email.Sender // nil: recipients and sign-in links cannot be emailed
	PDF      PDFRenderer  // nil: reports have no PDF, the web view is unaffected
	Drafter  Completer    // nil: no suggested summary; the team writes it
	HubURL   string       // public base of the hub, e.g. https://reports.vellatry.com
	Location *time.Location
	Now      func() time.Time
}

// Register adds the report jobs.
func (r *Reports) Register(ws *river.Workers) {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Location == nil {
		r.Location = time.UTC
	}
	river.AddWorker(ws, &reportScheduleAllWorker{r: r})
	river.AddWorker(ws, &reportDraftWorker{r: r})
	river.AddWorker(ws, &reportPDFWorker{r: r})
	river.AddWorker(ws, &reportNotifyWorker{r: r})
	river.AddWorker(ws, &hubLoginEmailWorker{r: r})
}

// PeriodicJobs checks hourly for series whose period is due (see Automation for why
// schedules are hourly checks rather than exact times).
func (r *Reports) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return jobargs.ReportScheduleAll{}, nil }, nil)}
}

type reportScheduleAllWorker struct {
	river.WorkerDefaults[jobargs.ReportScheduleAll]
	r *Reports
}

// Work drafts each automatic series once its latest period has ended and the lag for
// late data (Search Console settles in two to three days) has passed.
func (w *reportScheduleAllWorker) Work(ctx context.Context, _ *river.Job[jobargs.ReportScheduleAll]) error {
	now := w.r.Now().In(w.r.Location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var due []river.InsertManyParams
	err := db.InSystem(ctx, w.r.Pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id::text, id::text, period, draft_lag_days FROM report_series WHERE auto_draft AND NOT archived AND period <> 'custom'`)
		if err != nil {
			return err
		}
		type series struct {
			org, id, period string
			lag             int
		}
		all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (series, error) {
			var s series
			return s, r.Scan(&s.org, &s.id, &s.period, &s.lag)
		})
		if err != nil {
			return err
		}
		for _, s := range all {
			p, err := reports.LatestComplete(s.period, today)
			if err != nil || today.Before(p.End.AddDate(0, 0, s.lag+1)) {
				continue
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reports WHERE series_id::text = $1 AND period_start = $2 AND period_end = $3)`,
				s.id, p.Start, p.End).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				due = append(due, river.InsertManyParams{
					Args:       jobargs.ReportDraft{OrgID: s.org, SeriesID: s.id, Start: p.Start.Format(time.DateOnly), End: p.End.Format(time.DateOnly)},
					InsertOpts: &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}},
				})
			}
		}
		return nil
	})
	if err != nil || len(due) == 0 {
		return err
	}
	_, err = river.ClientFromContext[pgx.Tx](ctx).InsertMany(ctx, due)
	return err
}

type reportDraftWorker struct {
	river.WorkerDefaults[jobargs.ReportDraft]
	r *Reports
}

func (w *reportDraftWorker) Timeout(*river.Job[jobargs.ReportDraft]) time.Duration {
	return 5 * time.Minute
}

func (w *reportDraftWorker) Work(ctx context.Context, job *river.Job[jobargs.ReportDraft]) error {
	a, org := job.Args, job.Args.OrgID
	start, err1 := time.Parse(time.DateOnly, a.Start)
	end, err2 := time.Parse(time.DateOnly, a.End)
	if err1 != nil || err2 != nil {
		return river.JobCancel(fmt.Errorf("report draft: bad period %q to %q", a.Start, a.End))
	}
	var series reports.Series
	var snap reports.Snapshot
	err := db.InTenant(ctx, w.r.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if series, err = reports.LoadSeries(ctx, tx, a.SeriesID); err != nil {
			return err
		}
		p, err := reports.CustomPeriod(start, end)
		if err != nil {
			return err
		}
		if series.Period != reports.Custom {
			if p, err = reports.Containing(series.Period, start); err != nil {
				return err
			}
		}
		snap, err = reports.Build(ctx, tx, series.Sections, series.Period, p, w.r.Now())
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, reports.ErrBadRange) {
		return nil // series deleted, or a range the api should not have accepted
	}
	if err != nil {
		return err
	}

	// The absence rule: with nothing to report there is no report. The team hears why.
	if snap.Empty() {
		var reasons []string
		for _, n := range snap.Omitted {
			reasons = append(reasons, n.Reason)
		}
		return db.InTenant(ctx, w.r.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := w.r.Notify.record(ctx, tx, org, automation.Notification{
				Kind: "report_not_drafted", DedupeKey: "report_empty:" + series.ID + ":" + a.Start, Severity: "warning",
				Title: series.Name + " for " + snap.Period.Label + " was not drafted",
				Body:  "There was no data to report: " + strings.Join(reasons, "; ") + ".", Link: "/reports", Delivery: "immediate",
			})
			return err
		})
	}

	suggestion := w.r.suggestSummary(ctx, org, snap)
	return db.InTenant(ctx, w.r.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		id, created, err := reports.SaveDraft(ctx, tx, org, series, snap, suggestion, a.Refresh)
		if err != nil || id == "" {
			return err
		}
		if _, err := w.r.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.ReportDrafted, SubjectID: id, Actor: "reports",
			Payload: domainevents.ReportPayload{ReportID: id, SeriesID: series.ID, Start: a.Start, End: a.End}}); err != nil {
			return err
		}
		if !created {
			return nil
		}
		_, err = w.r.Notify.record(ctx, tx, org, automation.Notification{
			Kind: "report_drafted", DedupeKey: "report_drafted:" + id, Title: series.Name + " for " + snap.Period.Label + " is ready to review",
			Body: "Check the figures, write the summary and publish it to the reports hub.", Link: "/reports/" + id, Delivery: "immediate",
		})
		return err
	})
}

const summarySystem = `You draft the opening summary of a marketing performance report that an Australian in-house marketing team sends to its CMO. The team will edit your draft before anyone else sees it.

Rules:
- Use only the figures in the report below, written exactly as they appear there (same rounding, same units). Do not calculate new figures, totals, ratios or differences.
- Do not mention data that is not in the report, and do not speculate about causes the report does not show.
- Write three short paragraphs of two or three sentences each: AI visibility and blindspots, then search and site traffic, then what changed on the site and what to focus on next. Skip a paragraph if the report has nothing for it.
- Plain Australian English, no jargon, no hype, no headings, no bullet points, no emoji.`

// suggestSummary asks the model for a summary and keeps only the paragraphs whose every
// figure appears in the report. Any failure just means no suggestion.
func (r *Reports) suggestSummary(ctx context.Context, org string, snap reports.Snapshot) string {
	if r.Drafter == nil {
		return ""
	}
	body := reports.RenderText(snap)
	if len(body) > 24_000 {
		body = body[:24_000]
	}
	resp, err := r.Drafter.Complete(ctx, gateway.Request{
		Purpose: "report_draft", OrgID: org, System: summarySystem,
		Messages: []gateway.Message{{Role: "user", Content: "<report>\n" + body + "\n</report>\n\nDraft the summary."}},
		Schema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"paragraphs"},
			"properties": map[string]any{"paragraphs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		},
	})
	if err != nil {
		r.Logger.WarnContext(ctx, "summary draft skipped", "org_id", org, "error", err)
		return ""
	}
	var out struct {
		Paragraphs []string `json:"paragraphs"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil {
		r.Logger.WarnContext(ctx, "summary draft unreadable", "org_id", org, "error", err)
		return ""
	}
	kept, dropped := reports.KeepVerified(out.Paragraphs, reports.Figures(snap))
	if dropped > 0 {
		r.Logger.InfoContext(ctx, "summary paragraphs dropped for unverified figures", "org_id", org, "dropped", dropped)
	}
	return strings.Join(kept, "\n\n")
}

// ---- publishing ----------------------------------------------------------------------

type reportPDFWorker struct {
	river.WorkerDefaults[jobargs.ReportPDF]
	r *Reports
}

func (w *reportPDFWorker) Timeout(*river.Job[jobargs.ReportPDF]) time.Duration {
	return 3 * time.Minute
}

func (w *reportPDFWorker) Work(ctx context.Context, job *river.Job[jobargs.ReportPDF]) error {
	a := job.Args
	setStatus := func(status string, pdfBytes []byte) error {
		return db.InTenant(ctx, w.r.Pool, a.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE report_versions SET pdf_status = $3, pdf = $4 WHERE report_id::text = $1 AND version = $2`, a.ReportID, a.Version, status, pdfBytes)
			return err
		})
	}
	if w.r.PDF == nil {
		return setStatus("unavailable", nil)
	}
	var v reports.Version
	err := db.InTenant(ctx, w.r.Pool, a.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = reports.LoadVersion(ctx, tx, a.ReportID, a.Version)
		return err
	})
	if errors.Is(err, reports.ErrNotFound) {
		return nil // withdrawn meanwhile
	}
	if err != nil {
		return err
	}
	page, err := reports.RenderHTML(reports.View{Snapshot: v.Snapshot, Title: v.Title, Summary: v.Summary, Notes: v.Notes,
		Accent: v.Accent, Version: v.Version, PublishedAt: &v.PublishedAt, Print: true})
	if err != nil {
		return err
	}
	doc, err := w.r.PDF.HTMLToPDF(ctx, page)
	switch {
	case err == nil:
		return setStatus("ready", doc)
	case errors.Is(err, pdf.ErrRejected) || job.Attempt >= job.MaxAttempts:
		w.r.Logger.ErrorContext(ctx, "report pdf failed", "report_id", a.ReportID, "version", a.Version, "error", err)
		return setStatus("failed", nil)
	}
	return err
}

type reportNotifyWorker struct {
	river.WorkerDefaults[jobargs.ReportNotify]
	r *Reports
}

func (w *reportNotifyWorker) Work(ctx context.Context, job *river.Job[jobargs.ReportNotify]) error {
	a, org := job.Args, job.Args.OrgID
	var v reports.Version
	var series reports.Series
	var hub reports.Hub
	var brandName string
	var notified bool
	err := db.InTenant(ctx, w.r.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if v, err = reports.LoadVersion(ctx, tx, a.ReportID, a.Version); err != nil {
			return err
		}
		var seriesID string
		if err := tx.QueryRow(ctx, `SELECT series_id::text FROM reports WHERE id::text = $1`, a.ReportID).Scan(&seriesID); err != nil {
			return err
		}
		if series, err = reports.LoadSeries(ctx, tx, seriesID); err != nil {
			return err
		}
		if hub, err = reports.EnsureHub(ctx, tx, org); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM brands ORDER BY created_at LIMIT 1`).Scan(&brandName); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT notified_at IS NOT NULL FROM report_versions WHERE report_id::text = $1 AND version = $2`, a.ReportID, a.Version).Scan(&notified); err != nil {
			return err
		}
		// The team hears about every publish, in the dashboard and its destinations.
		verb := "was published"
		if a.Version > 1 {
			verb = fmt.Sprintf("was republished (revision %d)", a.Version)
		}
		_, err = w.r.Notify.record(ctx, tx, org, automation.Notification{
			Kind: "report_published", DedupeKey: fmt.Sprintf("report_published:%s:%d", a.ReportID, a.Version),
			Title: v.Title + " " + verb, Body: "It is in the reports hub.", Link: "/reports/" + a.ReportID, Delivery: "immediate",
		})
		return err
	})
	if errors.Is(err, reports.ErrNotFound) {
		return nil
	}
	if err != nil || !a.Email || notified || len(series.Recipients) == 0 {
		return err
	}
	if w.r.Email == nil {
		w.r.Logger.WarnContext(ctx, "report recipients not emailed: email is not configured", "org_id", org, "report_id", a.ReportID)
		return nil
	}
	link := strings.TrimRight(w.r.HubURL, "/") + "/hub/" + hub.Slug + "/reports/" + a.ReportID
	msg, err := reportReadyEmail(brandName, v, link)
	if err != nil {
		return err
	}
	msg.To = series.Recipients
	if err := w.r.Email.Send(ctx, msg); err != nil && !errors.Is(err, email.ErrRejected) {
		return err
	} else if err != nil {
		w.r.Logger.WarnContext(ctx, "report email rejected", "org_id", org, "report_id", a.ReportID, "error", err)
	}
	return db.InTenant(ctx, w.r.Pool, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE report_versions SET notified_at = now() WHERE report_id::text = $1 AND version = $2`, a.ReportID, a.Version)
		return err
	})
}

var reportEmailHTML = template.Must(template.New("report_email").Parse(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f6f7f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#101828">
<table role="presentation" width="100%" style="max-width:560px;margin:0 auto;background:#fff;border-radius:8px;border-top:4px solid {{.Accent}}">
<tr><td style="padding:24px">
<p style="margin:0 0 4px;color:#667085;font-size:14px">{{.Brand}}</p>
<h1 style="font-size:20px;margin:0 0 12px">{{.Title}}</h1>
<p style="font-size:15px;line-height:1.5;margin:0 0 20px">Your report for {{.Label}} is ready.</p>
<a href="{{.Link}}" style="display:inline-block;background:#101828;color:#fff;text-decoration:none;padding:10px 16px;border-radius:6px;font-size:14px">View the report</a>
<p style="font-size:13px;color:#667085;margin:20px 0 0">Sign in with your work email; no password needed. The link is private to your organisation.</p>
</td></tr></table></body></html>`))

func reportReadyEmail(brandName string, v reports.Version, link string) (email.Message, error) {
	accent := v.Accent
	if !reports.ValidAccent(accent) {
		accent = reports.DefaultAccent
	}
	var html strings.Builder
	if err := reportEmailHTML.Execute(&html, map[string]any{"Brand": brandName, "Title": v.Title, "Label": v.Snapshot.Period.Label,
		"Link": link, "Accent": template.CSS(accent)}); err != nil {
		return email.Message{}, err
	}
	text := fmt.Sprintf("%s\n\nYour report for %s is ready:\n%s\n\nSign in with your work email; no password needed.\n", v.Title, v.Snapshot.Period.Label, link)
	return email.Message{Subject: v.Title, Text: text, HTML: html.String(), Tag: "report"}, nil
}

// ---- hub sign-in -----------------------------------------------------------------------

type hubLoginEmailWorker struct {
	river.WorkerDefaults[jobargs.HubLoginEmail]
	r *Reports
}

// Work creates a sign-in link and emails it. The token exists only in the email: the
// database keeps its hash, and neither the event nor the job holds it.
func (w *hubLoginEmailWorker) Work(ctx context.Context, job *river.Job[jobargs.HubLoginEmail]) error {
	if w.r.Email == nil {
		w.r.Logger.WarnContext(ctx, "hub sign-in link not sent: email is not configured", "org_id", job.Args.OrgID)
		return nil
	}
	var token, brandName string
	var hub reports.Hub
	err := db.InTenant(ctx, w.r.Pool, job.Args.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if hub, err = reports.LoadHub(ctx, tx); err != nil {
			return err
		}
		if !hub.Enabled {
			return reports.ErrNotAllowed
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM brands ORDER BY created_at LIMIT 1`).Scan(&brandName); err != nil {
			return err
		}
		token, err = reports.CreateLogin(ctx, tx, job.Args.OrgID, job.Args.Email, w.r.Now())
		return err
	})
	if errors.Is(err, reports.ErrNotAllowed) || errors.Is(err, reports.ErrTooMany) || errors.Is(err, reports.ErrNotFound) {
		return nil // nothing is sent, and nothing tells the requester why
	}
	if err != nil {
		return err
	}
	link := strings.TrimRight(w.r.HubURL, "/") + "/hub/" + hub.Slug + "/auth?token=" + token
	text := fmt.Sprintf("Sign in to %s reports:\n%s\n\nThe link works once and expires in 20 minutes. If you did not ask for it, ignore this email.\n", brandName, link)
	html := `<p>Sign in to ` + template.HTMLEscapeString(brandName) + ` reports:</p><p><a href="` + template.HTMLEscapeString(link) +
		`">Sign in</a></p><p style="color:#667085;font-size:13px">The link works once and expires in 20 minutes. If you did not ask for it, ignore this email.</p>`
	err = w.r.Email.Send(ctx, email.Message{To: []string{strings.ToLower(job.Args.Email)}, Subject: "Your sign-in link for " + brandName + " reports",
		Text: text, HTML: html, Tag: "sign_in"})
	if errors.Is(err, email.ErrRejected) {
		w.r.Logger.WarnContext(ctx, "hub sign-in email rejected", "org_id", job.Args.OrgID, "error", err)
		return nil
	}
	return err
}
