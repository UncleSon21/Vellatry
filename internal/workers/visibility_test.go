package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/UncleSon21/vellatry/internal/api"
	"github.com/UncleSon21/vellatry/internal/dataforseo"
	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/events"
	"github.com/UncleSon21/vellatry/internal/platform/jobs"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// bestMattress is one of the questions setup writes for the topic "mattress" in Australia.
const bestMattress = "What is the best mattress in Australia?"

// fakeEngines stands in for DataForSEO's AI engines, the one thing this test does not run
// for real. A task is still queued the first time it is collected, as real ones usually
// are. The best-mattress question is answered with the competitor only; every other
// question names the brand.
type fakeEngines struct {
	mu    sync.Mutex
	asked []dataforseo.Request // in the order they were posted; task id is "dfs-<index>"
	polls map[string]int
}

func (f *fakeEngines) Live(context.Context, dataforseo.Request) (dataforseo.Answer, error) {
	return dataforseo.Answer{}, errors.New("live answers are not part of this flow")
}

func (f *fakeEngines) PostTasks(_ context.Context, reqs []dataforseo.Request) ([]dataforseo.Posted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]dataforseo.Posted, len(reqs))
	for i, r := range reqs {
		out[i] = dataforseo.Posted{TaskID: fmt.Sprintf("dfs-%d", len(f.asked)), Tag: r.Tag, Cost: 0.0012}
		f.asked = append(f.asked, r)
	}
	return out, nil
}

func (f *fakeEngines) GetTask(_ context.Context, e dataforseo.Engine, id string) (dataforseo.Answer, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	if _, err := fmt.Sscanf(id, "dfs-%d", &n); err != nil || n >= len(f.asked) || f.asked[n].Engine != e {
		return dataforseo.Answer{}, false, fmt.Errorf("no %s task %q was posted", e, id)
	}
	f.polls[id]++
	if f.polls[id] == 1 {
		return dataforseo.Answer{}, false, nil
	}
	a := dataforseo.Answer{Engine: e, TaskID: id, Present: true, Text: "Koala is a popular choice, and the Koala mattress ships fast."}
	if f.asked[n].Keyword == bestMattress {
		a.Text = "Ecosa is the best mattress in Australia. Many people like Ecosa."
		a.Sources = []dataforseo.Source{{URL: "https://www.ecosa.com.au/mattress", Domain: "ecosa.com.au"}}
	}
	return a, true, nil
}

func (f *fakeEngines) requests() []dataforseo.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dataforseo.Request(nil), f.asked...)
}

func (f *fakeEngines) collected(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls[id]
}

// TestBlindspotsFromSetupToDashboard is the product's main path, end to end: a team sets
// up its brand through the api, the worker asks the AI engines through the real job
// queue, waits for their answers, confirms where the brand is missing, and the dashboard's
// own endpoints show the blindspots and the numbers. Only DataForSEO is faked.
func TestBlindspotsFromSetupToDashboard(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	inserter, err := jobs.NewInserter(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(inserter, domainevents.Subscriptions()...)
	srv := httptest.NewServer((&api.Server{Pool: pool, Bus: bus, Verifier: api.DevVerifier{},
		Hub: &api.Hub{Pool: pool, Logger: slog.Default()}, Logger: slog.Default()}).Handler())
	t.Cleanup(srv.Close)

	stamp := time.Now().Format("150405.000000")
	email := "lead-" + stamp + "@example.com"
	call := func(method, path string, body, out any) int {
		t.Helper()
		var in io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			in = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, in)
		req.Header.Set("X-Dev-Email", email)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
		}
		return resp.StatusCode
	}

	// 1. Setup, as the wizard sends it. It writes four questions for the topic and queues
	// the first plan.
	var setup struct {
		OrgID string `json:"org_id"`
	}
	if code := call("POST", "/v1/onboarding", map[string]any{"org_name": "Koala Pty Ltd",
		"brand":       map[string]any{"name": "Koala", "domain": "koala-" + stamp + ".com"},
		"competitors": []map[string]any{{"name": "Ecosa", "domains": []string{"ecosa.com.au"}}},
		"topics":      []map[string]any{{"name": "mattress", "demand_monthly": 5000}}}, &setup); code != http.StatusCreated {
		t.Fatalf("onboarding: %d", code)
	}
	org := setup.OrgID
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'visibility_plan_org' AND args->>'org_id' = $1`, org).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("setup queued %d plans (%v), want 1", queued, err)
	}

	// 2. A worker, as production runs one, on a queue of its own: every job this client
	// inserts goes there, so it works only this test's jobs in a database other tests
	// share. A job due within a second is made available at once rather than waiting for
	// River's five-second scheduler, as River already does for a short snooze.
	engines := &fakeEngines{polls: map[string]int{}}
	v := &Visibility{Pool: pool, Answers: engines, Bus: bus, Logger: slog.Default(),
		CollectAfter: 10 * time.Millisecond, CollectEvery: 10 * time.Millisecond}
	workers := river.NewWorkers()
	v.Register(workers)
	queue := fmt.Sprintf("e2e_%d", time.Now().UnixNano())
	route := river.JobInsertMiddlewareFunc(func(ctx context.Context, many []*rivertype.JobInsertParams,
		inner func(context.Context) ([]*rivertype.JobInsertResult, error)) ([]*rivertype.JobInsertResult, error) {
		for _, p := range many {
			p.Queue = queue
			if p.State == rivertype.JobStateScheduled && p.ScheduledAt != nil && time.Until(*p.ScheduledAt) < time.Second {
				p.State = rivertype.JobStateAvailable
			}
		}
		return inner(ctx)
	})
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  map[string]river.QueueConfig{queue: {MaxWorkers: 4}},
		Workers: workers, Middleware: []rivertype.Middleware{route},
		FetchCooldown: 10 * time.Millisecond, FetchPollInterval: 50 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TestOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(stop)
	})

	// settle waits for every job on the queue to finish, and stops at the first that failed.
	settle := func() {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			var open, failed int
			var why string
			if err := pool.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE state NOT IN ('completed', 'cancelled', 'discarded')),
				       count(*) FILTER (WHERE state IN ('retryable', 'cancelled', 'discarded')),
				       coalesce(string_agg(errors::text, ' ') FILTER (WHERE state IN ('retryable', 'cancelled', 'discarded')), '')
				FROM river_job WHERE queue = $1`, queue).Scan(&open, &failed, &why); err != nil {
				t.Fatal(err)
			}
			switch {
			case failed > 0:
				t.Fatalf("%d jobs failed: %s", failed, why)
			case open == 0:
				return
			case time.Now().After(deadline):
				t.Fatalf("%d jobs still unfinished after 30s", open)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	// 3. Plans run until there is nothing left to ask today: the periodic planner does this
	// every fifteen minutes in production. Screening first, then confirming only where the
	// brand was missing, then nothing.
	var rounds [][]dataforseo.Request
	for len(rounds) < 4 {
		before := len(engines.requests())
		if _, err := client.Insert(ctx, jobargs.VisibilityPlanOrg{OrgID: org}, nil); err != nil {
			t.Fatal(err)
		}
		settle()
		asked := engines.requests()[before:]
		rounds = append(rounds, asked)
		if len(asked) == 0 {
			break
		}
	}
	if len(rounds) != 3 || len(rounds[0]) == 0 || len(rounds[1]) == 0 {
		sizes := make([]int, len(rounds))
		for i, r := range rounds {
			sizes[i] = len(r)
		}
		t.Fatalf("questions asked per plan = %v, want screening, confirmation, then none", sizes)
	}
	confirmedOn := map[dataforseo.Engine]bool{}
	for _, r := range rounds[1] {
		if r.Keyword != bestMattress {
			t.Errorf("confirmation asked %q again, where the brand was already named", r.Keyword)
		}
		confirmedOn[r.Engine] = true
	}
	if len(confirmedOn) != 3 {
		t.Errorf("confirmation asked %d engines, want all three", len(confirmedOn))
	}

	// Every task was waited for once, then collected once: no answer was asked for twice.
	all := engines.requests()
	for i := range all {
		if n := engines.collected(fmt.Sprintf("dfs-%d", i)); n != 2 {
			t.Errorf("task dfs-%d was collected %d times, want 2 (not ready, then the answer)", i, n)
		}
	}
	var snoozed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE queue = $1 AND kind = 'visibility_collect' AND metadata->>'snoozes' = '1'`, queue).Scan(&snoozed); err != nil || snoozed != len(all) {
		t.Errorf("%d of %d collections snoozed once while the answer was not ready (%v)", snoozed, len(all), err)
	}

	// 4. What the dashboard shows. Only the best-mattress question is a blindspot, on every
	// engine, confirmed, with the competitor that took the answer.
	var blindspots []struct {
		Prompt     string  `json:"prompt"`
		Engine     string  `json:"engine"`
		Confirmed  bool    `json:"confirmed"`
		Competitor *string `json:"competitor"`
	}
	if code := call("GET", "/v1/visibility/blindspots", nil, &blindspots); code != http.StatusOK || len(blindspots) == 0 {
		t.Fatalf("blindspots: %d, %d of them", code, len(blindspots))
	}
	onEngine := map[string]bool{}
	competitorNamed := false
	for _, b := range blindspots {
		if b.Prompt != bestMattress {
			t.Errorf("a blindspot on %q, where every engine named the brand", b.Prompt)
		}
		if !b.Confirmed {
			t.Errorf("an unconfirmed blindspot on %s after confirmation finished", b.Engine)
		}
		onEngine[b.Engine] = true
		competitorNamed = competitorNamed || (b.Competitor != nil && *b.Competitor == "Ecosa")
	}
	if len(onEngine) != 3 || !competitorNamed {
		t.Errorf("blindspots on %v, competitor named: %v; want all three engines and Ecosa", onEngine, competitorNamed)
	}

	// The numbers are counts of what the engines said, nothing else.
	mentioned := 0
	for _, r := range all {
		if r.Keyword != bestMattress {
			mentioned++
		}
	}
	var perf struct {
		Overall struct {
			Answers, Present, Mentioned int
			Visibility                  *float64
		} `json:"overall"`
	}
	if code := call("GET", "/v1/visibility/performance", nil, &perf); code != http.StatusOK {
		t.Fatalf("performance: %d", code)
	}
	o := perf.Overall
	if o.Answers != len(all) || o.Present != len(all) || o.Mentioned != mentioned {
		t.Errorf("performance shows %d answers, %d present, %d mentioning the brand; the engines gave %d, %d, %d",
			o.Answers, o.Present, o.Mentioned, len(all), len(all), mentioned)
	}
	if want := 100 * float64(mentioned) / float64(len(all)); o.Visibility == nil || math.Abs(*o.Visibility-want) > 0.005 {
		t.Errorf("visibility shows %v, want %.2f%%", o.Visibility, want)
	}

	// The live stream saw the blindspot open, and every question asked was paid for once.
	var timeline []struct {
		Kind string `json:"kind"`
	}
	call("GET", "/v1/events?limit=200", nil, &timeline)
	opened := false
	for _, e := range timeline {
		opened = opened || e.Kind == domainevents.BlindspotOpened
	}
	if !opened {
		t.Error("no blindspot-opened event reached the event stream")
	}
	var paid int
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM usage_ledger WHERE org_id = $1 AND provider = 'dataforseo'`, org).Scan(&paid)
	}); err != nil || paid != len(all) {
		t.Errorf("the ledger holds %d DataForSEO charges (%v), want one per question asked: %d", paid, err, len(all))
	}
}
