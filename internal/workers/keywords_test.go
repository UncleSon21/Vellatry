package workers

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/dataforseo"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/keywords"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// fakeResearch stands in for DataForSEO: two keywords that share their results and one
// that does not, plus a competitor's ranking keyword.
type fakeResearch struct {
	mu    sync.Mutex
	serps map[string]dataforseo.OrganicSERP // by task id
	seeds []string
	posts int
}

func (f *fakeResearch) KeywordIdeas(_ context.Context, seeds []string, loc int, lang string, limit int) ([]dataforseo.KeywordData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seeds = seeds
	d := func(v int) *int { return &v }
	return []dataforseo.KeywordData{
		{Keyword: "mattress in a box", SearchVolume: 5400, Difficulty: d(38), Intent: "commercial",
			Monthly: []dataforseo.MonthVolume{{Year: 2026, Month: 8, Volume: 5400}}},
		{Keyword: "bed in a box", SearchVolume: 2900, Difficulty: d(35), Intent: "commercial"},
		{Keyword: "mattress topper", SearchVolume: 1600, Difficulty: d(20), Intent: "commercial"},
	}, nil
}

func (f *fakeResearch) RankedKeywords(_ context.Context, domain string, loc int, lang string, limit int) ([]dataforseo.RankedKeyword, error) {
	return []dataforseo.RankedKeyword{
		{KeywordData: dataforseo.KeywordData{Keyword: "ecosa mattress review", SearchVolume: 800, Intent: "commercial"}, Rank: 2, URL: "https://ecosa.com.au/review"},
		{KeywordData: dataforseo.KeywordData{Keyword: "best mattress australia", SearchVolume: 3200, Intent: "commercial"}, Rank: 4, URL: "https://ecosa.com.au/best"},
	}, nil
}

func (f *fakeResearch) PostOrganic(_ context.Context, reqs []dataforseo.OrganicRequest) ([]dataforseo.Posted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts++
	shared := []string{"https://ecosa.com.au/mattress", "https://sleepingduck.com/m", "https://choice.com.au/mattress", "https://koala.com/products/mattress-in-a-box"}
	out := make([]dataforseo.Posted, len(reqs))
	for i, r := range reqs {
		id := "task-" + strings.ReplaceAll(r.Keyword, " ", "-")
		out[i] = dataforseo.Posted{TaskID: id, Tag: r.Tag, Cost: 0.0006}
		serp := dataforseo.OrganicSERP{TaskID: id, Keyword: r.Keyword, Features: []string{"people_also_ask"}}
		switch r.Keyword {
		case "mattress in a box", "bed in a box", "best mattress australia":
			serp.URLs = shared
		default:
			serp.URLs = []string{"https://other.example/" + id}
		}
		f.serps[id] = serp
	}
	return out, nil
}

func (f *fakeResearch) GetOrganic(_ context.Context, taskID string) (dataforseo.OrganicSERP, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.serps[taskID]
	return s, ok, nil
}

func TestKeywordResearch(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, brandID := testdb.NewOrg(t, pool, "research")
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, domainevents.Subscriptions()...)
	auto := &Automation{Pool: pool, Bus: bus, Logger: slog.Default(), Slack: &fakeSlack{}}
	auto.Register(river.NewWorkers())
	fake := &fakeResearch{serps: map[string]dataforseo.OrganicSERP{}}

	var queued []river.JobArgs
	var qmu sync.Mutex
	k := &Keywords{Pool: pool, Bus: bus, Logger: slog.Default(), Notify: auto, Research: fake,
		Now: func() time.Time { return time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC) },
		Enqueue: func(_ context.Context, args river.JobArgs) error {
			qmu.Lock()
			defer qmu.Unlock()
			queued = append(queued, args)
			return nil
		}}
	k.Register(river.NewWorkers())
	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO topics (org_id, brand_id, name, source, status) VALUES ($1, $2, 'mattress', 'manual', 'active')`, org, brandID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO competitors (org_id, brand_id, name, domains) VALUES ($1, $2, 'Ecosa', $3)`, org, brandID, []string{"ecosa.com.au"})
		return err
	})

	// The run: seeds, ideas, the competitor gap, then queued search results.
	if err := (&keywordRunWorker{k: k}).Work(ctx, job(jobargs.KeywordRun{OrgID: org, Trigger: "manual", Seeds: []string{"sofa bed"}})); err != nil {
		t.Fatal(err)
	}
	if !contains(fake.seeds, "mattress") || !contains(fake.seeds, "sofa bed") {
		t.Errorf("seeds sent = %v; the team's topics and the asked-for seeds both count", fake.seeds)
	}
	var runID int64
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		runs, err := keywords.ListRuns(ctx, tx, 5)
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs = %v, %v", runs, err)
		}
		runID = runs[0].ID
		if runs[0].Status != "collecting" {
			t.Errorf("run status = %s", runs[0].Status)
		}
		var total, rejected int
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'rejected') FROM keywords`).Scan(&total, &rejected); err != nil {
			return err
		}
		if total < 6 || rejected != 1 {
			t.Errorf("keywords = %d with %d rejected; a competitor's own brand search is rejected", total, rejected)
		}
		var reason string
		if err := tx.QueryRow(ctx, `SELECT coalesce(reject_reason, '') FROM keywords WHERE keyword = 'ecosa mattress review'`).Scan(&reason); err != nil {
			return err
		}
		if reason != keywords.RejectCompetitorBrand {
			t.Errorf("reject reason = %q", reason)
		}
		var comp []byte
		if err := tx.QueryRow(ctx, `SELECT competitors FROM keywords WHERE keyword = 'best mattress australia'`).Scan(&comp); err != nil {
			return err
		}
		if !strings.Contains(string(comp), `"ecosa.com.au":4`) {
			t.Errorf("competitor positions = %s", comp)
		}
		var queuedTasks int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM serp_tasks WHERE run_id = $1 AND status = 'queued'`, runID).Scan(&queuedTasks)
		if queuedTasks == 0 {
			t.Error("no search results were queued")
		}
		return err
	})

	// Collecting: each result arrives, and the last one starts clustering.
	qmu.Lock()
	collects := append([]river.JobArgs(nil), queued...)
	queued = nil
	qmu.Unlock()
	collect := &keywordCollectWorker{k: k}
	for _, args := range collects {
		a, ok := args.(jobargs.KeywordSERPCollect)
		if !ok {
			t.Fatalf("unexpected job %T", args)
		}
		if err := collect.Work(ctx, job(a)); err != nil {
			t.Fatal(err)
		}
	}
	qmu.Lock()
	after := append([]river.JobArgs(nil), queued...)
	qmu.Unlock()
	clusterJobs := 0
	for _, a := range after {
		if _, ok := a.(jobargs.KeywordCluster); ok {
			clusterJobs++
		}
	}
	if clusterJobs != 1 {
		t.Fatalf("clustering jobs enqueued = %d, want exactly one when the last result lands", clusterJobs)
	}

	// Clustering: topics, a page for each, a score and the issues.
	if err := (&keywordClusterWorker{k: k}).Work(ctx, job(jobargs.KeywordCluster{OrgID: org, RunID: runID})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT name, status, keyword_count, coalesce(intent, ''), opportunity, issues FROM topics WHERE status = 'proposed' ORDER BY name`)
		if err != nil {
			return err
		}
		type row struct {
			name, status, intent string
			count                int
			opp, issues          []byte
		}
		got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			return x, r.Scan(&x.name, &x.status, &x.count, &x.intent, &x.opp, &x.issues)
		})
		if err != nil {
			return err
		}
		if len(got) < 2 {
			t.Fatalf("proposed topics = %+v", got)
		}
		var head row
		for _, g := range got {
			if g.name == "mattress in a box" {
				head = g
			}
		}
		if head.name == "" || head.count != 3 {
			t.Errorf("the shared-results topic = %+v, want three keywords under the highest-volume name", head)
		}
		var opp keywords.Opportunity
		if err := json.Unmarshal(head.opp, &opp); err != nil {
			return err
		}
		if opp.Volume != 11500 || opp.Score <= 0 || opp.Target != keywords.TargetPosition {
			t.Errorf("opportunity = %+v", opp)
		}
		var issues []keywords.Issue
		if err := json.Unmarshal(head.issues, &issues); err != nil {
			return err
		}
		if len(issues) != 1 || issues[0].Kind != keywords.IssueNoPage {
			t.Errorf("issues = %+v; nothing has been crawled, so no page owns it yet", issues)
		}
		var assigned int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM keywords WHERE topic_id IS NOT NULL`).Scan(&assigned); err != nil {
			return err
		}
		if assigned < 4 {
			t.Errorf("keywords assigned to topics = %d", assigned)
		}
		var status string
		var stats []byte
		if err := tx.QueryRow(ctx, `SELECT status, stats FROM keyword_runs WHERE id = $1`, runID).Scan(&status, &stats); err != nil {
			return err
		}
		if status != "done" || !strings.Contains(string(stats), `"clusters"`) {
			t.Errorf("run = %s %s", status, stats)
		}
		var proposed int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'topics_proposed'`).Scan(&proposed)
		if proposed != 1 {
			t.Errorf("notifications about new topics = %d", proposed)
		}
		return err
	})

	// A second run reuses the topics rather than proposing them again.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE keyword_runs SET status = 'done', finished_at = now() WHERE id = $1`, runID)
		return err
	})
	if err := (&keywordRunWorker{k: k}).Work(ctx, job(jobargs.KeywordRun{OrgID: org, Trigger: "schedule"})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var second int64
		if err := tx.QueryRow(ctx, `SELECT id FROM keyword_runs ORDER BY id DESC LIMIT 1`).Scan(&second); err != nil {
			return err
		}
		var queuedAgain int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM serp_tasks WHERE run_id = $1`, second).Scan(&queuedAgain); err != nil {
			return err
		}
		if queuedAgain != 0 {
			t.Errorf("the second run re-fetched %d results that are still fresh", queuedAgain)
		}
		return nil
	})
	if err := (&keywordClusterWorker{k: k}).Work(ctx, job(jobargs.KeywordCluster{OrgID: org, RunID: runID})); err != nil {
		t.Fatal(err)
	}
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var topics int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM topics WHERE status = 'proposed'`).Scan(&topics)
		if topics > 3 {
			t.Errorf("proposed topics after a second clustering = %d; the same clusters must match their topics", topics)
		}
		return err
	})
}

func contains(list []string, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}
