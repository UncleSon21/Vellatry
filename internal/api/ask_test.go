package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/reports"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// The hub side of the report bot: a question is recorded and handed to a worker, a reader
// sees their own questions and only their own, and only the person who asked can send one
// to the team.
func TestHubQuestions(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Pool: pool, Bus: events.NewBus(inserter, domainevents.Subscriptions()...), Verifier: DevVerifier{},
		Hub: &Hub{Pool: pool, Logger: slog.Default()}, Logger: slog.Default()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	s.HubURL = srv.URL

	stamp := time.Now().Format("150405.000000")
	domain := "wombat-" + stamp + ".com"
	team := client{t, srv, "lead-" + stamp + "@" + domain}
	code, body, _ := team.do("POST", "/v1/onboarding", map[string]any{"org_name": "Wombat Pty Ltd",
		"brand": map[string]any{"name": "Wombat", "domain": domain}})
	if code != http.StatusCreated {
		t.Fatalf("onboarding: %d %v", code, body)
	}
	org := body["org_id"].(string)
	cmo, cfo := "cmo@"+domain, "cfo@"+domain

	snap := reports.Snapshot{Version: reports.SnapshotVersion, Org: "Wombat Pty Ltd", Brand: "Wombat",
		Period:   reports.Period{Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Label: "August 2026"},
		Previous: reports.Period{Label: "July 2026"}, Order: []string{reports.SecVisibility},
		Visibility: &reports.VisibilitySection{Answers: 300, Visibility: reports.Pair{Current: ptr(41.0), Previous: ptr(35.5)}}}
	var reportID string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var seriesID string
		if err := tx.QueryRow(ctx, `INSERT INTO report_series (org_id, name, period, recipients) VALUES ($1, 'Monthly performance', 'month', $2) RETURNING id::text`,
			org, []string{cmo, cfo}).Scan(&seriesID); err != nil {
			return err
		}
		sr, err := reports.LoadSeries(ctx, tx, seriesID)
		if err != nil {
			return err
		}
		if reportID, _, err = reports.SaveDraft(ctx, tx, org, sr, snap, "", false); err != nil {
			return err
		}
		_, err = reports.Publish(ctx, tx, org, reportID, "lead")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, body, _ = team.do("GET", "/v1/hub", nil)
	base := strings.TrimPrefix(body["url"].(string), srv.URL)
	report := base + "/reports/" + reportID
	qs := func(id int64) string { return strconv.FormatInt(id, 10) }

	// signIn returns a browser already holding a hub session for one address.
	signIn := func(email string) (func(string) (int, string, http.Header), func(string, url.Values, string) (int, string)) {
		t.Helper()
		get, post := hubBrowser(t, srv.URL)
		var token string
		if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			token, err = reports.CreateLogin(ctx, tx, org, email, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if code, _ := post(base+"/auth", url.Values{"token": {token}}, srv.URL); code != http.StatusSeeOther {
			t.Fatalf("sign in as %s: %d", email, code)
		}
		return get, post
	}
	getCMO, postCMO := signIn(cmo)

	// The panel is on the report, and says where answers come from.
	code, page, _ := getCMO(report)
	if code != http.StatusOK || !strings.Contains(page, "Ask about this report") || !strings.Contains(page, "Answers come only from this report") {
		t.Fatalf("report page: %d %s", code, page)
	}

	// A cross-site post is refused, and so is one with no session.
	if code, _ := postCMO(report+"/ask", url.Values{"question": {"Why?"}}, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-site question: %d", code)
	}
	_, postAnon := hubBrowser(t, srv.URL)
	if code, _ := postAnon(report+"/ask", url.Values{"question": {"Why?"}}, srv.URL); code != http.StatusSeeOther {
		t.Errorf("a question with no session: %d, want a redirect to sign in", code)
	}

	// An empty or over-long question is told so rather than stored.
	if code, _ := postCMO(report+"/ask", url.Values{"question": {"   "}}, srv.URL); code != http.StatusSeeOther {
		t.Errorf("empty question: %d", code)
	}
	if code, _ := postCMO(report+"/ask", url.Values{"question": {strings.Repeat("why ", 100)}}, srv.URL); code != http.StatusSeeOther {
		t.Errorf("over-long question: %d", code)
	}
	if _, page, _ := getCMO(report + "?ask=empty"); !strings.Contains(page, "Type a question first") {
		t.Error("an empty question produced no message")
	}
	if _, page, _ := getCMO(report + "?ask=long"); !strings.Contains(page, "under 300 characters") {
		t.Error("an over-long question produced no message")
	}
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var stored int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM report_questions`).Scan(&stored)
		if stored != 0 {
			t.Errorf("questions stored before a usable one was asked = %d", stored)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// A real question is stored against the published version, with an event for the worker.
	if code, _ := postCMO(report+"/ask", url.Values{"question": {"Why did visibility move?"}, "v": {"1"}}, srv.URL); code != http.StatusSeeOther {
		t.Fatalf("question: %d", code)
	}
	var qid int64
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		asked, err := reports.QuestionsBy(ctx, tx, reportID, 1, cmo)
		if err != nil {
			return err
		}
		if len(asked) != 1 || asked[0].Question != "Why did visibility move?" || asked[0].Status != "asked" {
			t.Fatalf("stored questions = %+v", asked)
		}
		qid = asked[0].ID
		var queued int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1 AND subject_id = $2`,
			domainevents.ReportQuestionAsked, qs(qid)).Scan(&queued)
		if queued != 1 {
			t.Errorf("events for the answer worker = %d", queued)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, page, _ := getCMO(report); !strings.Contains(page, "Why did visibility move?") || !strings.Contains(page, "Working on it") {
		t.Error("the reader cannot see their own question waiting")
	}

	// A question asked of a version that was never published is refused.
	if code, _ := postCMO(report+"/ask", url.Values{"question": {"And in July?"}, "v": {"7"}}, srv.URL); code != http.StatusNotFound {
		t.Errorf("a question against an unpublished version: %d", code)
	}

	// The worker's half, done here: the answer appears, with the follow-up offer.
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return reports.SaveAnswer(ctx, tx, qid, "Visibility was 41.0% in August 2026.", "answered", 0)
	}); err != nil {
		t.Fatal(err)
	}
	_, page, _ = getCMO(report)
	if !strings.Contains(page, "Visibility was 41.0% in August 2026.") || !strings.Contains(page, "Ask the team to look into this") {
		t.Error("the answer or the follow-up offer is missing")
	}

	// A colleague at the same company does not see it, and cannot escalate it.
	getCFO, postCFO := signIn(cfo)
	if _, page, _ := getCFO(report); strings.Contains(page, "Why did visibility move?") {
		t.Error("one reader can see another reader's questions")
	}
	if code, _ := postCFO(report+"/follow-up", url.Values{"question": {qs(qid)}}, srv.URL); code != http.StatusNotFound {
		t.Errorf("a colleague escalated someone else's question: %d", code)
	}

	// The person who asked can, once, and the team is told once.
	if code, _ := postCMO(report+"/follow-up", url.Values{"question": {qs(qid)}}, srv.URL); code != http.StatusSeeOther {
		t.Fatalf("follow-up: %d", code)
	}
	if code, _ := postCMO(report+"/follow-up", url.Values{"question": {qs(qid)}}, srv.URL); code != http.StatusSeeOther {
		t.Errorf("a second follow-up: %d", code)
	}
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		q, err := reports.LoadQuestion(ctx, tx, qid)
		if err != nil {
			return err
		}
		if q.FollowUp == nil {
			t.Error("the follow-up was not recorded")
		}
		var raised int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1 AND subject_id = $2`,
			domainevents.ReportFollowUpAsked, qs(qid)).Scan(&raised)
		if raised != 1 {
			t.Errorf("follow-up events = %d, want one however many times the form is sent", raised)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, page, _ := getCMO(report); !strings.Contains(page, "Sent to the team") || strings.Contains(page, "Ask the team to look into this") {
		t.Error("the panel still offers a follow-up that has been sent")
	}

	// The team sees every reader's questions on the report they wrote.
	code, _, list := team.do("GET", "/v1/reports/"+reportID+"/questions", nil)
	if code != http.StatusOK || len(list) != 1 {
		t.Fatalf("questions for the team: %d %v", code, list)
	}
	q := list[0].(map[string]any)
	if q["question"] != "Why did visibility move?" || q["asked_by"] != cmo || q["status"] != "answered" || q["follow_up"] == nil {
		t.Errorf("the writer's view of the question = %v", q)
	}
}
