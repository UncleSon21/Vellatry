package dataforseo

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
)

func TestKeywordIdeas(t *testing.T) {
	var body []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/dataforseo_labs/google/keyword_ideas/live") {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"status_code":20000,"cost":0.06,"tasks":[{"id":"t","status_code":20000,"result":[{"items":[
			{"keyword":"mattress in a box","keyword_info":{"search_volume":5400,"cpc":2.35,"competition":0.42,
			  "monthly_searches":[{"year":2026,"month":8,"search_volume":5400},{"year":2026,"month":7,"search_volume":4900}]},
			 "keyword_properties":{"keyword_difficulty":38},"search_intent_info":{"main_intent":"commercial"}},
			{"keyword":"", "keyword_info":{"search_volume":10}}]}]}]}`))
	}, budget.New(1, 0.001))

	out, err := c.KeywordIdeas(context.Background(), []string{"mattress"}, 2036, "en", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("ideas = %+v (a keyword with no text must be dropped)", out)
	}
	k := out[0]
	if k.Keyword != "mattress in a box" || k.SearchVolume != 5400 || *k.Difficulty != 38 || k.Intent != "commercial" ||
		*k.CPC != 2.35 || len(k.Monthly) != 2 || k.Monthly[0].Volume != 5400 {
		t.Errorf("keyword = %+v", k)
	}
	if len(body) != 1 || body[0]["location_code"].(float64) != 2036 || body[0]["language_code"] != "en" || body[0]["limit"].(float64) != 500 {
		t.Errorf("request body = %v", body)
	}
	if _, err := c.KeywordIdeas(context.Background(), nil, 2036, "en", 500); err != nil {
		t.Errorf("no seeds should not call the API: %v", err)
	}
}

func TestRankedKeywords(t *testing.T) {
	var body []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"status_code":20000,"cost":0.04,"tasks":[{"id":"t","status_code":20000,"result":[{"items":[
			{"keyword_data":{"keyword":"best mattress australia","keyword_info":{"search_volume":2900},
			  "search_intent_info":{"main_intent":"commercial"}},
			 "ranked_serp_element":{"serp_item":{"rank_absolute":3,"url":"https://ecosa.com.au/best"}}}]}]}]}`))
	}, budget.New(1, 0.001))

	out, err := c.RankedKeywords(context.Background(), "https://www.ecosa.com.au", 2036, "en", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Keyword != "best mattress australia" || out[0].Rank != 3 || out[0].URL != "https://ecosa.com.au/best" {
		t.Fatalf("ranked = %+v", out)
	}
	if body[0]["target"] != "ecosa.com.au" {
		t.Errorf("target = %v; the scheme and www must be stripped", body[0]["target"])
	}
}

func TestOrganicTasks(t *testing.T) {
	queued := true
	var posted []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/serp/google/organic/task_post"):
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.Write([]byte(`{"status_code":20000,"cost":0.0012,"tasks":[{"id":"s1","status_code":20100,"cost":0.0006},{"id":"s2","status_code":20100,"cost":0.0006}]}`))
		case strings.Contains(r.URL.Path, "/serp/google/organic/task_get/regular/s1"):
			if queued {
				queued = false
				w.Write([]byte(`{"status_code":20000,"tasks":[{"id":"s1","status_code":40602,"status_message":"Task In Queue."}]}`))
				return
			}
			w.Write([]byte(`{"status_code":20000,"tasks":[{"id":"s1","status_code":20000,"cost":0,"data":{"tag":"7","keyword":"mattress"},
				"result":[{"keyword":"mattress","item_types":["organic","shopping","people_also_ask"],"items":[
					{"type":"organic","url":"https://koala.com/mattress"},
					{"type":"shopping","url":"https://ads.example/x"},
					{"type":"organic","url":"https://ecosa.com.au/mattress"}]}]}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}, budget.New(1, 0.001))
	ctx := context.Background()

	out, err := c.PostOrganic(ctx, []OrganicRequest{{Keyword: "mattress", LocationCode: 2036, LanguageCode: "en", Tag: "7"}, {Keyword: "sofa", LocationCode: 2036, LanguageCode: "en"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].TaskID != "s1" || out[0].Err != nil {
		t.Fatalf("posted = %+v", out)
	}
	if posted[0]["depth"].(float64) != 10 || posted[0]["tag"] != "7" {
		t.Errorf("post body = %v", posted[0])
	}
	if _, ready, err := c.GetOrganic(ctx, "s1"); err != nil || ready {
		t.Fatalf("queued task: ready=%v err=%v", ready, err)
	}
	serp, ready, err := c.GetOrganic(ctx, "s1")
	if err != nil || !ready {
		t.Fatalf("ready task: %v %v", ready, err)
	}
	if strings.Join(serp.URLs, " ") != "https://koala.com/mattress https://ecosa.com.au/mattress" {
		t.Errorf("urls = %v; only organic results, in order", serp.URLs)
	}
	if strings.Join(serp.Features, ",") != "shopping,people_also_ask" || serp.Keyword != "mattress" || serp.Tag != "7" {
		t.Errorf("serp = %+v", serp)
	}
}

func TestLabsBudgetIsSizedForSERPUnits(t *testing.T) {
	// A Labs call costs about as much as a hundred queued SERPs; reserving one unit
	// would let a run slip past its cap.
	if labsUnits(500) < 100 || labsUnits(0) < 10 {
		t.Errorf("labsUnits(500) = %d, labsUnits(0) = %d", labsUnits(500), labsUnits(0))
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the call must fail before it is sent")
	}, budget.New(0.01, 0.0006)) // a cent, at SERP-sized units
	if _, err := c.KeywordIdeas(context.Background(), []string{"mattress"}, 2036, "en", 500); err == nil {
		t.Error("a Labs call over the budget was sent")
	}
}
