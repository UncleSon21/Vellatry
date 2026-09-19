package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"golang.org/x/oauth2"

	"github.com/UncleSon21/vellatry/internal/asana"
	"github.com/UncleSon21/vellatry/internal/automation"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/platform/secrets"
	"github.com/UncleSon21/vellatry/internal/slack"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

func TestDigestWeek(t *testing.T) {
	syd, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 9, 21, 7, 59, 0, 0, syd), "2026-09-14"}, // Monday before 8am: last week's is still the latest due
		{time.Date(2026, 9, 21, 8, 0, 0, 0, syd), "2026-09-21"},
		{time.Date(2026, 9, 23, 15, 0, 0, 0, syd), "2026-09-21"},       // catches up mid-week
		{time.Date(2026, 9, 27, 23, 0, 0, 0, syd), "2026-09-21"},       // Sunday
		{time.Date(2026, 9, 20, 22, 30, 0, 0, time.UTC), "2026-09-21"}, // 8:30am Monday in Sydney
	}
	for _, c := range cases {
		if got := DigestWeek(c.now, syd, 8); got != c.want {
			t.Errorf("DigestWeek(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

type fakeSlack struct {
	mu    sync.Mutex
	posts []string
	err   error
}

func (f *fakeSlack) Post(_ context.Context, url string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.posts = append(f.posts, url+" "+string(payload))
	return nil
}

func (f *fakeSlack) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestAutomationFlow(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "watch")
	box, err := secrets.NewBox(testKey)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSlack{}
	a := &Automation{Pool: pool, Box: box, Slack: fs, Logger: slog.Default(), AppURL: "https://app.test",
		Bus: events.NewBus(inserter, domainevents.Subscriptions()...)}
	a.Register(river.NewWorkers())
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	hook, _ := box.Seal(org, []byte("https://hooks.slack.com/services/T/B/X"))
	var destID string
	var ev events.Stored
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if err := automation.InsertDefaults(ctx, tx, org); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO destinations (org_id, kind, name, secret) VALUES ($1, 'slack', '#marketing', $2) RETURNING id::text`, org, hook).Scan(&destID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_findings (org_id, fingerprint, rule, severity, message) VALUES ($1, 'fp1', 'robots_blocks_search_bot', 'critical', 'robots.txt blocks OAI-SearchBot')`, org); err != nil {
			return err
		}
		ev, err = a.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.SiteCrawlCompleted, SubjectID: "1", Actor: "crawler",
			Payload: domainevents.SiteCrawlCompletedPayload{Pages: 3, Opened: 1, CriticalOpened: []string{"fp1"}}})
		return err
	})

	// The watcher raises one notification; running it again merges into it.
	watch := &watchEventWorker{a: a}
	for i := 0; i < 2; i++ {
		if err := watch.Work(ctx, job(jobargs.WatchEvent{OrgID: org, EventID: ev.ID})); err != nil {
			t.Fatal(err)
		}
	}
	var nid int64
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var occ int
		var delivery, severity string
		if err := tx.QueryRow(ctx, `SELECT id, occurrences, delivery, severity FROM notifications WHERE kind = 'critical_finding'`).Scan(&nid, &occ, &delivery, &severity); err != nil {
			return err
		}
		if occ != 2 || delivery != "immediate" || severity != "critical" {
			t.Errorf("notification: occurrences %d, delivery %s, severity %s", occ, delivery, severity)
		}
		var created int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1`, domainevents.NotificationCreated).Scan(&created)
		if created != 1 {
			t.Errorf("notification.created events = %d, want 1 (repeats are merged)", created)
		}
		return err
	})

	// Delivery sends once, however often the job runs.
	deliver := &notifyDeliverWorker{a: a}
	for i := 0; i < 2; i++ {
		if err := deliver.Work(ctx, job(jobargs.NotifyDeliver{OrgID: org, NotificationID: nid})); err != nil {
			t.Fatal(err)
		}
	}
	if fs.count() != 1 || !strings.Contains(fs.posts[0], "OAI-SearchBot") || !strings.HasPrefix(fs.posts[0], "https://hooks.slack.com/services/T/B/X ") {
		t.Fatalf("slack posts = %v", fs.posts)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM notifications WHERE id = $1`, nid).Scan(&status)
		if status != "sent" {
			t.Errorf("status = %s, want sent", status)
		}
		return err
	})

	// A webhook Slack no longer accepts marks the destination broken, visibly.
	fs.err = fmt.Errorf("%w (no_service)", slack.ErrGone)
	var n2 int64
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		r, err := automation.Record(ctx, tx, org, automation.Notification{Kind: "connection_broken", DedupeKey: "t2", Title: "GA4 needs reconnecting", Delivery: "immediate"})
		n2 = r.ID
		return err
	})
	if err := deliver.Work(ctx, job(jobargs.NotifyDeliver{OrgID: org, NotificationID: n2})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var dstatus, nstatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM destinations WHERE id = $1`, destID).Scan(&dstatus); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status FROM notifications WHERE id = $1`, n2).Scan(&nstatus); err != nil {
			return err
		}
		if dstatus != "broken" || nstatus != "failed" {
			t.Errorf("destination %s, notification %s; want broken and failed", dstatus, nstatus)
		}
		var broken int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE kind = $1 AND payload->>'kind' = 'slack'`, domainevents.ConnectionBroken).Scan(&broken)
		if broken != 1 {
			t.Errorf("connection.broken events for slack = %d", broken)
		}
		_, _ = tx.Exec(ctx, `UPDATE destinations SET status = 'active' WHERE id = $1`, destID)
		return err
	})
	fs.err = nil

	// The digest: last week's figures plus the alerts that were set to wait for it.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		for d := 0; d < 14; d++ {
			day := time.Date(2026, 9, 7+d, 0, 0, 0, 0, time.UTC)
			mentioned := 10
			if d >= 7 {
				mentioned = 5
			}
			if _, err := tx.Exec(ctx, `INSERT INTO visibility_daily (org_id, day, engine, answers, present, mentioned, method_version) VALUES ($1, $2, 'chatgpt', 20, 20, $3, 'vis-1')`,
				org, day, mentioned); err != nil {
				return err
			}
		}
		_, err := automation.Record(ctx, tx, org, automation.Notification{Kind: "blindspot_confirmed", DedupeKey: "b1", Title: "Blindspot confirmed on Gemini", Body: "left out", Delivery: "digest"})
		return err
	})
	digest := &digestOrgWorker{a: a}
	for i := 0; i < 2; i++ {
		if err := digest.Work(ctx, job(jobargs.DigestOrg{OrgID: org, Week: "2026-09-21"})); err != nil {
			t.Fatal(err)
		}
	}
	var did int64
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT id, data FROM notifications WHERE kind = 'digest'`).Scan(&did, &data); err != nil {
			return err
		}
		var d automation.Digest
		_ = json.Unmarshal(data, &d)
		if d.From != "2026-09-14" || d.To != "2026-09-20" || d.Visibility == nil || *d.Visibility.Visibility != 25 || *d.Visibility.Previous != 50 {
			t.Errorf("digest = %s", data)
		}
		if len(d.Alerts) != 1 || d.Search != nil || d.Site != nil {
			t.Errorf("digest sections: alerts %d, search %v, site %v (no data must mean no section)", len(d.Alerts), d.Search, d.Site)
		}
		var waiting string
		err := tx.QueryRow(ctx, `SELECT status FROM notifications WHERE dedupe_key = 'b1'`).Scan(&waiting)
		if waiting != "digested" {
			t.Errorf("included alert status = %s", waiting)
		}
		return err
	})
	before := fs.count()
	if err := deliver.Work(ctx, job(jobargs.NotifyDeliver{OrgID: org, NotificationID: did})); err != nil {
		t.Fatal(err)
	}
	if fs.count() != before+1 || !strings.Contains(fs.posts[len(fs.posts)-1], "watch weekly") {
		t.Errorf("digest not posted: %v", fs.posts[before:])
	}
}

func TestImmediateAlertsAreThrottled(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "throttle")
	err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		for i := 0; i <= automation.MaxImmediatePerHour; i++ {
			r, err := automation.Record(ctx, tx, org, automation.Notification{Kind: "critical_finding", DedupeKey: fmt.Sprint(i), Title: "x", Delivery: "immediate"})
			if err != nil {
				return err
			}
			want := "immediate"
			if i == automation.MaxImmediatePerHour {
				want = "digest"
			}
			if r.Delivery != want {
				t.Errorf("alert %d delivery = %s, want %s", i, r.Delivery, want)
			}
		}
		r, err := automation.Record(ctx, tx, org, automation.Notification{Kind: "digest", DedupeKey: "digest:w", Title: "digest", Delivery: "immediate"})
		if r.Delivery != "immediate" {
			t.Error("the digest itself must never be throttled")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ---- Asana ---------------------------------------------------------------------------

type fakeAsana struct {
	mu       sync.Mutex
	tasks    map[string]*asana.Task // by external id
	byGID    map[string]*asana.Task
	comments []string
	creates  int
}

func newFakeAsana() *fakeAsana {
	return &fakeAsana{tasks: map[string]*asana.Task{}, byGID: map[string]*asana.Task{}}
}

func (f *fakeAsana) Workspaces(context.Context) ([]asana.Ref, error) {
	return []asana.Ref{{GID: "1", Name: "Koala"}}, nil
}
func (f *fakeAsana) Projects(_ context.Context, ws string) ([]asana.Ref, error) {
	return []asana.Ref{{GID: "10", Name: "Marketing", Workspace: ws}}, nil
}
func (f *fakeAsana) TaskByExternal(_ context.Context, ext string) (asana.Task, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[ext]; ok {
		return *t, true, nil
	}
	return asana.Task{}, false, nil
}
func (f *fakeAsana) CreateTask(_ context.Context, nt asana.NewTask) (asana.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	t := &asana.Task{GID: fmt.Sprint(100 + f.creates), URL: "https://app.asana.com/0/10/" + fmt.Sprint(100+f.creates)}
	f.tasks[nt.ExternalID], f.byGID[t.GID] = t, t
	return *t, nil
}
func (f *fakeAsana) SetCompleted(_ context.Context, gid string, done bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byGID[gid].Completed = done
	return nil
}
func (f *fakeAsana) Comment(_ context.Context, gid, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, gid+": "+text)
	return nil
}

func TestAsanaTaskLifecycle(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "asana")
	box, err := secrets.NewBox(testKey)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	fa := newFakeAsana()
	s := &Asana{Pool: pool, Box: box, Logger: slog.Default(), AppURL: "https://app.test",
		Bus:       events.NewBus(inserter, domainevents.Subscriptions()...),
		NewClient: func(context.Context, string) AsanaAPI { return fa },
		Exchange:  func(context.Context, string) (*oauth2.Token, error) { return &oauth2.Token{RefreshToken: "rt"}, nil },
		Revoke:    func(context.Context, string) error { return nil },
	}
	s.Register(river.NewWorkers())
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}
	code, _ := box.Seal(org, []byte("code"))
	var fixID string
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO connections (org_id, kind, status, secret) VALUES ($1, 'asana', 'pending', $2)`, org, code); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO fixes (org_id, source, fingerprint, title, instructions, snippet)
			VALUES ($1, 'audit', 'fp9', 'Let OAI-SearchBot crawl the site', 'Edit robots.txt.', 'User-agent: OAI-SearchBot') RETURNING id::text`, org).Scan(&fixID)
	})
	if err := (&asanaAuthorizeWorker{s: s}).Work(ctx, job(jobargs.AsanaAuthorize{OrgID: org})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		g, err := loadAsana(ctx, tx)
		if err != nil {
			return err
		}
		if g.Status != "connected" || !g.Config.Token || !g.Config.Listed || len(g.Config.Projects) != 1 {
			t.Errorf("after authorize: %s %+v", g.Status, g.Config)
		}
		// What PUT /v1/connections/asana/project does, then POST /v1/fixes/{id}/asana.
		if _, err := tx.Exec(ctx, `UPDATE connections SET config = config || '{"project": "10"}' WHERE kind = 'asana'`); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO task_links (org_id, source, subject_id) VALUES ($1, 'fix', $2)`, org, fixID)
		return err
	})

	create := &asanaCreateTaskWorker{s: s}
	for i := 0; i < 2; i++ {
		if err := create.Work(ctx, job(jobargs.AsanaCreateTask{OrgID: org, Source: "fix", SubjectID: fixID})); err != nil {
			t.Fatal(err)
		}
	}
	if fa.creates != 1 {
		t.Fatalf("tasks created = %d, want 1", fa.creates)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var link, fix, route string
		if err := tx.QueryRow(ctx, `SELECT status FROM task_links WHERE subject_id = $1`, fixID).Scan(&link); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status, route FROM fixes WHERE id::text = $1`, fixID).Scan(&fix, &route); err != nil {
			return err
		}
		if link != "open" || fix != "sent" || route != "asana" {
			t.Errorf("link %s, fix %s via %s", link, fix, route)
		}
		// The next crawl confirms the fix is live.
		_, err := tx.Exec(ctx, `UPDATE fixes SET status = 'live', live_at = now() WHERE id::text = $1`, fixID)
		return err
	})
	closeDone := &asanaCloseDoneWorker{s: s}
	for i := 0; i < 2; i++ {
		if err := closeDone.Work(ctx, job(jobargs.AsanaCloseDone{OrgID: org})); err != nil {
			t.Fatal(err)
		}
	}
	ext := externalID(org, "fix", fixID)
	if !fa.tasks[ext].Completed || len(fa.comments) != 1 || !strings.Contains(fa.comments[0], "live site") {
		t.Fatalf("after close: completed %v, comments %v", fa.tasks[ext].Completed, fa.comments)
	}

	// The problem comes back and the team sends it again: the same task is reopened.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE fixes SET status = 'proposed' WHERE id::text = $1`, fixID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE task_links SET status = 'creating' WHERE subject_id = $1`, fixID)
		return err
	})
	if err := create.Work(ctx, job(jobargs.AsanaCreateTask{OrgID: org, Source: "fix", SubjectID: fixID})); err != nil {
		t.Fatal(err)
	}
	if fa.creates != 1 || fa.tasks[ext].Completed || len(fa.comments) != 2 {
		t.Errorf("regression: creates %d, completed %v, comments %v", fa.creates, fa.tasks[ext].Completed, fa.comments)
	}
}
