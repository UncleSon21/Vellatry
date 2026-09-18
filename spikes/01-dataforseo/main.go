// Spike 1: what do AI answers for Australian queries cost, how fast are they, how often
// do they cite sources, and how well does deterministic brand detection work?
//
// It runs every configured query on each engine for N attempts, writes one JSON line per
// answer, and prints a summary with a monthly cost projection per plan scenario.
// A hard budget stops it before it can overspend. See README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/UncleSon21/vellatry/internal/dataforseo"
	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/visibility/detect"
	"github.com/UncleSon21/vellatry/internal/visibility/gap"
)

type config struct {
	LocationCode int        `json:"location_code"`
	LanguageCode string     `json:"language_code"`
	Sets         []querySet `json:"sets"`
}

type querySet struct {
	Name        string          `json:"name"`
	Brand       detect.Entity   `json:"brand"`
	Competitors []detect.Entity `json:"competitors"`
	Queries     []string        `json:"queries"`
}

type options struct {
	configPath  string
	engines     []dataforseo.Engine
	attempts    int
	mode        string
	webSearch   string
	asyncAIO    bool
	budgetUSD   float64
	estimateUSD float64
	concurrency int
	outDir      string
	dryRun      bool
	pollEvery   time.Duration
	maxWait     time.Duration
	proj        projection
}

// job is one planned answer.
type job struct {
	id      string
	set     *querySet
	variant string
	attempt int
	req     dataforseo.Request
}

// Record is one line of results.jsonl.
type Record struct {
	ID                   string      `json:"id"`
	Set                  string      `json:"set"`
	Brand                string      `json:"brand"`
	Query                string      `json:"query"`
	Engine               string      `json:"engine"`
	Variant              string      `json:"variant"`
	Attempt              int         `json:"attempt"`
	Mode                 string      `json:"mode"`
	OK                   bool        `json:"ok"`
	Error                string      `json:"error,omitempty"`
	LatencyMS            int64       `json:"latency_ms"`
	Cost                 float64     `json:"cost"`
	Present              bool        `json:"present"`
	AsyncPending         bool        `json:"async_pending,omitempty"`
	TextChars            int         `json:"text_chars"`
	Sources              []string    `json:"sources"`
	SearchResults        int         `json:"search_results"`
	FanOut               []string    `json:"fan_out,omitempty"`
	BrandEntities        []string    `json:"brand_entities,omitempty"`
	Model                string      `json:"model,omitempty"`
	BrandMentioned       bool        `json:"brand_mentioned"`
	BrandCount           int         `json:"brand_count"`
	BrandPosition        int         `json:"brand_position"`
	BrandCited           bool        `json:"brand_cited"`
	CompetitorsMentioned []string    `json:"competitors_mentioned,omitempty"`
	CitedCompetitors     []string    `json:"cited_competitors,omitempty"`
	Verdict              gap.Verdict `json:"verdict"`
	Text                 string      `json:"text"`
}

func main() {
	opts, err := parseFlags()
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := loadConfig(opts.configPath)
	if err != nil {
		log.Fatal(err)
	}

	httpClient := http.DefaultClient
	login, password := os.Getenv("DATAFORSEO_LOGIN"), os.Getenv("DATAFORSEO_PASSWORD")
	if opts.dryRun {
		httpClient = &http.Client{Transport: newFakeTransport(cfg)}
		login, password = "dry-run", "dry-run"
	} else if login == "" || password == "" {
		log.Fatal("set DATAFORSEO_LOGIN and DATAFORSEO_PASSWORD, or pass -dry-run")
	}

	spend := budget.New(opts.budgetUSD, opts.estimateUSD)
	client, err := dataforseo.New(dataforseo.Config{
		Login: login, Password: password, HTTPClient: httpClient,
		Concurrency: opts.concurrency, Budget: spend,
	})
	if err != nil {
		log.Fatal(err)
	}

	jobs := plan(cfg, opts)
	log.Printf("planned %d answers (%s mode, budget $%.2f)", len(jobs), opts.mode, opts.budgetUSD)

	if err := os.MkdirAll(opts.outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	sink, err := newSink(filepath.Join(opts.outDir, "results.jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	defer sink.close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if opts.mode == "standard" {
		runStandard(ctx, client, jobs, opts, sink)
	} else {
		runLive(ctx, client, jobs, opts, sink)
	}

	summary := summarize(sink.all(), opts, len(jobs), spend.Spent())
	if err := os.WriteFile(filepath.Join(opts.outDir, "summary.md"), []byte(summary), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println(summary)
	log.Printf("wrote %s", opts.outDir)
}

func parseFlags() (options, error) {
	var o options
	var engines string
	flag.StringVar(&o.configPath, "config", "config.example.json", "query sets and brands")
	flag.StringVar(&engines, "engines", "chatgpt,gemini,ai_overview", "comma-separated engines")
	flag.IntVar(&o.attempts, "attempts", 2, "attempts per query and engine (5 = confirmation depth)")
	flag.StringVar(&o.mode, "mode", "live", "live (fast lane) or standard (queue, cheaper)")
	flag.StringVar(&o.webSearch, "web-search", "off", "ChatGPT force_web_search: off, on or both")
	flag.BoolVar(&o.asyncAIO, "async-aio", false, "ask DataForSEO to wait for asynchronously loaded AI Overviews")
	flag.Float64Var(&o.budgetUSD, "budget-usd", 3, "hard spend cap in USD; the run stops before exceeding it")
	flag.Float64Var(&o.estimateUSD, "estimate-usd", 0.02, "per-task reservation until real costs are seen")
	flag.IntVar(&o.concurrency, "concurrency", 4, "max requests in flight")
	flag.StringVar(&o.outDir, "out", filepath.Join("results", time.Now().Format("20060102-150405")), "output directory")
	flag.BoolVar(&o.dryRun, "dry-run", false, "use a fake API (no cost, no credentials)")
	flag.DurationVar(&o.pollEvery, "poll", 15*time.Second, "standard mode: task_get polling interval")
	flag.DurationVar(&o.maxWait, "max-wait", 30*time.Minute, "standard mode: give up on tasks after this long")
	flag.IntVar(&o.proj.topics, "proj-topics", 40, "projection: topics per tenant")
	flag.IntVar(&o.proj.promptsPerTopic, "proj-prompts", 3, "projection: discovery prompts per topic per month")
	flag.Float64Var(&o.proj.confirmShare, "proj-confirm", 0.3, "projection: share of prompts escalated to 5-attempt confirmation")
	flag.IntVar(&o.proj.tracked, "proj-tracked", 100, "projection: tracked prompts refreshed weekly at 5 attempts")
	flag.Parse()

	for _, s := range strings.Split(engines, ",") {
		e, err := dataforseo.ParseEngine(s)
		if err != nil {
			return o, err
		}
		o.engines = append(o.engines, e)
	}
	if o.mode != "live" && o.mode != "standard" {
		return o, fmt.Errorf("-mode must be live or standard")
	}
	if o.webSearch != "off" && o.webSearch != "on" && o.webSearch != "both" {
		return o, fmt.Errorf("-web-search must be off, on or both")
	}
	if o.attempts < 1 {
		return o, fmt.Errorf("-attempts must be at least 1")
	}
	return o, nil
}

func loadConfig(path string) (config, error) {
	var cfg config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.LocationCode == 0 {
		cfg.LocationCode = 2036
	}
	if cfg.LanguageCode == "" {
		cfg.LanguageCode = "en"
	}
	return cfg, nil
}

func plan(cfg config, o options) []job {
	var jobs []job
	for si := range cfg.Sets {
		set := &cfg.Sets[si]
		for qi, q := range set.Queries {
			for _, e := range o.engines {
				for _, variant := range variants(e, o.webSearch) {
					for a := 1; a <= o.attempts; a++ {
						id := fmt.Sprintf("s%d-q%d-%s-%s-a%d", si, qi, e, variant, a)
						jobs = append(jobs, job{
							id: id, set: set, variant: variant, attempt: a,
							req: dataforseo.Request{
								Engine: e, Keyword: q, Tag: id,
								LocationCode: cfg.LocationCode, LanguageCode: cfg.LanguageCode,
								ForceWebSearch:      variant == "web_search_on",
								LoadAsyncAIOverview: o.asyncAIO,
							},
						})
					}
				}
			}
		}
	}
	return jobs
}

func variants(e dataforseo.Engine, webSearch string) []string {
	if e != dataforseo.ChatGPT {
		return []string{"default"}
	}
	switch webSearch {
	case "on":
		return []string{"web_search_on"}
	case "both":
		return []string{"web_search_off", "web_search_on"}
	}
	return []string{"web_search_off"}
}

func runLive(ctx context.Context, c *dataforseo.Client, jobs []job, o options, sink *sink) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < o.concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				start := time.Now()
				a, err := c.Live(ctx, j.req)
				if errors.Is(err, budget.ErrExceeded) {
					log.Printf("budget reached, stopping: %v", err)
					cancel()
					return
				}
				if errors.Is(err, context.Canceled) {
					continue // cancelled mid-call: nothing was answered, nothing to record
				}
				sink.add(record(j, "live", a, err, time.Since(start)))
			}
		}()
	}
	for _, j := range jobs {
		select {
		case queue <- j:
		case <-ctx.Done():
			close(queue)
			wg.Wait()
			return
		}
	}
	close(queue)
	wg.Wait()
}

func runStandard(ctx context.Context, c *dataforseo.Client, jobs []job, o options, sink *sink) {
	type pending struct {
		j      job
		taskID string
		cost   float64
		posted time.Time
	}
	var waiting []pending
	byEngine := map[dataforseo.Engine][]job{}
	for _, j := range jobs {
		byEngine[j.req.Engine] = append(byEngine[j.req.Engine], j)
	}
	postAll := func() {
		for _, list := range byEngine {
			for i := 0; i < len(list); i += 100 {
				batch := list[i:min(i+100, len(list))]
				reqs := make([]dataforseo.Request, len(batch))
				for k, j := range batch {
					reqs[k] = j.req
				}
				posted, err := c.PostTasks(ctx, reqs)
				if errors.Is(err, budget.ErrExceeded) {
					log.Printf("budget reached, no more tasks posted: %v", err)
					return
				}
				if err != nil {
					log.Printf("post failed for %d tasks: %v", len(batch), err)
					for _, j := range batch {
						sink.add(record(j, "standard", dataforseo.Answer{}, err, 0))
					}
					continue
				}
				now := time.Now()
				for k, p := range posted {
					if p.Err != nil {
						sink.add(record(batch[k], "standard", dataforseo.Answer{}, p.Err, 0))
						continue
					}
					waiting = append(waiting, pending{batch[k], p.TaskID, p.Cost, now})
				}
			}
		}
	}
	postAll()

	deadline := time.Now().Add(o.maxWait)
	for len(waiting) > 0 && time.Now().Before(deadline) && ctx.Err() == nil {
		select {
		case <-time.After(o.pollEvery):
		case <-ctx.Done():
		}
		var still []pending
		for _, p := range waiting {
			a, ready, err := c.GetTask(ctx, p.j.req.Engine, p.taskID)
			switch {
			case err != nil && dataforseo.IsTransient(err):
				still = append(still, p)
			case err != nil || ready:
				if a.Cost == 0 {
					a.Cost = p.cost
				}
				sink.add(record(p.j, "standard", a, err, time.Since(p.posted)))
			default:
				still = append(still, p)
			}
		}
		waiting = still
		log.Printf("%d tasks still queued", len(waiting))
	}
	for _, p := range waiting {
		sink.add(record(p.j, "standard", dataforseo.Answer{}, fmt.Errorf("not ready after %s", o.maxWait), time.Since(p.posted)))
	}
}

func record(j job, mode string, a dataforseo.Answer, err error, latency time.Duration) Record {
	r := Record{
		ID: j.id, Set: j.set.Name, Brand: j.set.Brand.Name, Query: j.req.Keyword, Engine: string(j.req.Engine),
		Variant: j.variant, Attempt: j.attempt, Mode: mode, LatencyMS: latency.Milliseconds(),
	}
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.OK = true
	r.Cost = a.Cost
	r.Present, r.AsyncPending = a.Present, a.AsyncPending
	r.TextChars, r.Text = len(a.Text), a.Text
	r.SearchResults, r.FanOut, r.BrandEntities, r.Model = a.SearchResults, a.FanOut, a.BrandEntities, a.Model
	for _, s := range a.Sources {
		r.Sources = append(r.Sources, s.URL)
	}
	if !a.Present {
		return r
	}
	res := detect.Analyze(a.Text, r.Sources, j.set.Brand, j.set.Competitors)
	r.BrandMentioned, r.BrandCount, r.BrandPosition, r.BrandCited = res.Mentioned(), res.Brand.Count, res.BrandPosition, res.BrandCited
	r.CitedCompetitors = res.CitedCompetitors
	for _, m := range res.Competitors {
		if m.Count > 0 {
			r.CompetitorsMentioned = append(r.CompetitorsMentioned, m.Entity)
		}
	}
	r.Verdict = gap.Classify(gap.Signals{
		BrandMentioned:      res.Mentioned(),
		BrandCited:          res.BrandCited,
		SourcesExist:        len(r.Sources) > 0,
		CompetitorMentioned: res.CompetitorMentioned(),
		CompetitorCited:     len(res.CitedCompetitors) > 0,
	})
	return r
}

// sink appends records to results.jsonl as they arrive, so an interrupted run keeps
// everything it paid for.
type sink struct {
	mu      sync.Mutex
	f       *os.File
	enc     *json.Encoder
	records []Record
}

func newSink(path string) (*sink, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &sink{f: f, enc: json.NewEncoder(f)}, nil
}

func (s *sink) add(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	if err := s.enc.Encode(r); err != nil {
		log.Printf("write result: %v", err)
	}
	status := "ok"
	if !r.OK {
		status = "error: " + r.Error
	}
	log.Printf("%-40s %6dms $%.4f %s", r.ID, r.LatencyMS, r.Cost, status)
}

func (s *sink) all() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...)
}

func (s *sink) close() { s.f.Close() }
