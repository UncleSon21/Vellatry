package main

import (
	"strings"
	"testing"

	"github.com/UncleSon21/vellatry/internal/dataforseo"
)

func TestProjectionMonthlyCalls(t *testing.T) {
	d, tr := projection{topics: 40, promptsPerTopic: 3, confirmShare: 0.3, tracked: 100}.monthlyCalls()
	if d != 420 { // 120 prompts x (2 + 0.3 x 5)
		t.Errorf("discover = %v, want 420", d)
	}
	if tr != 2165 { // 100 x 5 x 4.33
		t.Errorf("track = %v, want 2165", tr)
	}
}

func TestPercentile(t *testing.T) {
	v := []int64{5, 1, 4, 2, 3}
	if got := percentile(v, 0.5); got != 3 {
		t.Errorf("p50 = %d", got)
	}
	if got := percentile(v, 0.95); got != 5 {
		t.Errorf("p95 = %d", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("empty = %d", got)
	}
}

func TestPlanVariants(t *testing.T) {
	cfg := config{LocationCode: 2036, LanguageCode: "en", Sets: []querySet{{Name: "s", Queries: []string{"a", "b"}}}}
	o := options{engines: []dataforseo.Engine{dataforseo.ChatGPT, dataforseo.AIOverview}, attempts: 2, webSearch: "both"}
	jobs := plan(cfg, o)
	// 2 queries x (ChatGPT x 2 variants + AI Overview x 1) x 2 attempts
	if len(jobs) != 12 {
		t.Fatalf("jobs = %d, want 12", len(jobs))
	}
	on := 0
	for _, j := range jobs {
		if j.req.ForceWebSearch {
			on++
			if j.req.Engine != dataforseo.ChatGPT {
				t.Errorf("web search forced on %s", j.req.Engine)
			}
		}
	}
	if on != 4 {
		t.Errorf("web search on = %d, want 4", on)
	}
}

func TestSummarizeReportsGapsAndProjection(t *testing.T) {
	recs := []Record{
		{ID: "1", Set: "s", Brand: "Koala", Query: "q", Engine: "chatgpt", Variant: "web_search_off", OK: true, Present: true, Cost: 0.004, LatencyMS: 1000, BrandMentioned: true, BrandEntities: []string{"Ecosa"}},
		{ID: "2", Set: "s", Brand: "Koala", Query: "q", Engine: "chatgpt", Variant: "web_search_off", OK: true, Present: true, Cost: 0.004, LatencyMS: 3000},
		{ID: "3", Set: "s", Brand: "Koala", Query: "q", Engine: "chatgpt", Variant: "web_search_off", Error: "boom"},
	}
	o := options{mode: "live", attempts: 2, budgetUSD: 3, engines: []dataforseo.Engine{dataforseo.ChatGPT},
		proj: projection{topics: 1, promptsPerTopic: 1, tracked: 0}}
	out := summarize(recs, o, 3, 0.008)
	for _, want := range []string{"| chatgpt | web_search_off | 2 | 1 |", "1 disagree", "0.0040", "| 1 | chatgpt: boom |"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}
