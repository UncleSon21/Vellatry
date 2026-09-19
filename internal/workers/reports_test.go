package workers

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/email"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/reports"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

type fakeEmail struct {
	mu   sync.Mutex
	sent []email.Message
}

func (f *fakeEmail) Send(_ context.Context, m email.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

type fakeDrafter struct {
	calls int
	reply string
}

func (f *fakeDrafter) Complete(_ context.Context, req gateway.Request) (gateway.Response, error) {
	f.calls++
	if req.Purpose != "report_draft" || !strings.Contains(req.Messages[0].Content, "## AI visibility") {
		return gateway.Response{}, nil
	}
	return gateway.Response{Text: f.reply}, nil
}

type fakePDF struct{ calls int }

func (f *fakePDF) HTMLToPDF(_ context.Context, html []byte) ([]byte, error) {
	f.calls++
	if !strings.Contains(string(html), "@page") {
		return nil, nil
	}
	return []byte("%PDF-fake"), nil
}

func TestReportLifecycle(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "report")
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, domainevents.Subscriptions()...)
	mail := &fakeEmail{}
	drafter := &fakeDrafter{reply: `{"paragraphs": ["Koala appeared in 40.0% of AI answers in August 2026, down 10.0 pts on July.", "Revenue grew 99% thanks to AI."]}`}
	auto := &Automation{Pool: pool, Bus: bus, Logger: slog.Default(), Slack: &fakeSlack{}}
	auto.Register(river.NewWorkers())
	pdfs := &fakePDF{}
	r := &Reports{Pool: pool, Bus: bus, Logger: slog.Default(), Notify: auto, Email: mail, PDF: pdfs, Drafter: drafter,
		HubURL: "https://reports.test", Now: func() time.Time { return time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC) }}
	r.Register(river.NewWorkers())
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	var domain, seriesID string
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT domain FROM brands`).Scan(&domain); err != nil {
			return err
		}
		for day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC); day.Month() <= time.August; day = day.AddDate(0, 0, 1) {
			mentioned := 10 // July: 50%
			if day.Month() == time.August {
				mentioned = 8 // August: 40%
			}
			if _, err := tx.Exec(ctx, `INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, method_version) VALUES ($1, $2, 'chatgpt', 20, 20, $3, 'vis-1')`,
				org, day, mentioned); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO report_series (org_id, name, period, recipients) VALUES ($1, 'Monthly performance', 'month', $2) RETURNING id::text`,
			org, []string{"cmo@" + domain}).Scan(&seriesID)
	})

	// The draft: sections without data are left out, and the suggested summary keeps only
	// the paragraph whose figures the report shows.
	draft := &reportDraftWorker{r: r}
	if err := draft.Work(ctx, job(jobargs.ReportDraft{OrgID: org, SeriesID: seriesID, Start: "2026-08-01", End: "2026-08-31"})); err != nil {
		t.Fatal(err)
	}
	var reportID string
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		list, err := reports.ListReports(ctx, tx, seriesID, "", 10)
		if err != nil || len(list) != 1 {
			t.Fatalf("reports = %v, %v", list, err)
		}
		reportID = list[0].ID
		rep, err := reports.LoadReport(ctx, tx, reportID)
		if err != nil {
			return err
		}
		if strings.Join(rep.Snapshot.Order, ",") != "visibility,blindspots" {
			t.Errorf("sections = %v; omitted %v", rep.Snapshot.Order, rep.Snapshot.Omitted)
		}
		if rep.Title != "Monthly performance: August 2026" || *rep.Snapshot.Visibility.Visibility.Current != 40 || *rep.Snapshot.Visibility.Visibility.Previous != 50 {
			t.Errorf("report = %s %+v", rep.Title, rep.Snapshot.Visibility.Visibility)
		}
		if rep.SummaryDraft == nil || *rep.SummaryDraft != "Koala appeared in 40.0% of AI answers in August 2026, down 10.0 pts on July." {
			t.Errorf("suggested summary = %v", rep.SummaryDraft)
		}
		if rep.Summary != "" {
			t.Error("the suggestion must not become the summary without the team")
		}
		return nil
	})
	// A period with no data produces no report, and the team is told why.
	if err := draft.Work(ctx, job(jobargs.ReportDraft{OrgID: org, SeriesID: seriesID, Start: "2025-01-01", End: "2025-01-31"})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var n, notes int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM reports`).Scan(&n); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'report_not_drafted'`).Scan(&notes)
		if n != 1 || notes != 1 {
			t.Errorf("reports %d, not-drafted notices %d", n, notes)
		}
		return err
	})

	// Publish, render the PDF, tell the recipients once.
	var version int
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		version, err = reports.Publish(ctx, tx, org, reportID, "lead")
		return err
	})
	if err := (&reportPDFWorker{r: r}).Work(ctx, job(jobargs.ReportPDF{OrgID: org, ReportID: reportID, Version: version})); err != nil {
		t.Fatal(err)
	}
	notify := &reportNotifyWorker{r: r}
	for i := 0; i < 2; i++ {
		if err := notify.Work(ctx, job(jobargs.ReportNotify{OrgID: org, ReportID: reportID, Version: version, Email: true})); err != nil {
			t.Fatal(err)
		}
	}
	if len(mail.sent) != 1 || mail.sent[0].To[0] != "cmo@"+domain || !strings.Contains(mail.sent[0].Text, "https://reports.test/hub/") ||
		!strings.Contains(mail.sent[0].Text, "/reports/"+reportID) {
		t.Fatalf("recipient emails = %+v", mail.sent)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		pdf, err := reports.VersionPDF(ctx, tx, reportID, version)
		if err != nil || string(pdf) != "%PDF-fake" {
			t.Errorf("pdf = %q, %v", pdf, err)
		}
		var published int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'report_published'`).Scan(&published)
		if published != 1 {
			t.Errorf("team notices = %d", published)
		}
		return err
	})

	// Sign-in links go only to addresses that may view the hub, and only the hash is kept.
	login := &hubLoginEmailWorker{r: r}
	for _, addr := range []string{"cmo@" + domain, "stranger@gmail.com"} {
		if err := login.Work(ctx, job(jobargs.HubLoginEmail{OrgID: org, Email: addr})); err != nil {
			t.Fatal(err)
		}
	}
	if len(mail.sent) != 2 {
		t.Fatalf("emails = %d, want the report email and one sign-in link", len(mail.sent))
	}
	link := mail.sent[1].Text
	i := strings.Index(link, "token=")
	if mail.sent[1].To[0] != "cmo@"+domain || i < 0 {
		t.Fatalf("sign-in email = %+v", mail.sent[1])
	}
	token := strings.Fields(link[i+len("token="):])[0]
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var stored string
		if err := tx.QueryRow(ctx, `SELECT token_hash FROM hub_logins WHERE email = $1`, "cmo@"+domain).Scan(&stored); err != nil {
			return err
		}
		if stored == token || stored != reports.HashToken(token) {
			t.Error("the sign-in token must be stored hashed")
		}
		return nil
	})
}
