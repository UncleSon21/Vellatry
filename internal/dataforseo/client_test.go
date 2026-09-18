package dataforseo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestClient(t *testing.T, h http.HandlerFunc, b *budget.Budget) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Login: "l", Password: "p", BaseURL: srv.URL, Budget: b, CallTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestLiveChatGPTParsesAndSendsBody(t *testing.T) {
	var gotPath string
	var gotBody []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if u, p, ok := r.BasicAuth(); !ok || u != "l" || p != "p" {
			t.Errorf("basic auth missing")
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Write(fixture(t, "chatgpt_live.json"))
	}, budget.New(1, 0.01))

	a, err := c.Live(context.Background(), Request{Engine: ChatGPT, Keyword: "best mattress in a box Australia", LocationCode: 2036, LanguageCode: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v3/ai_optimization/chat_gpt/llm_scraper/live/advanced" {
		t.Errorf("path = %s", gotPath)
	}
	if len(gotBody) != 1 || gotBody[0]["force_web_search"] != false || gotBody[0]["location_code"] != float64(2036) {
		t.Errorf("body = %v", gotBody)
	}
	if !a.Present || !strings.Contains(a.Text, "Koala") || !strings.Contains(a.Text, "Emma Sleep") {
		t.Errorf("text = %q", a.Text)
	}
	if len(a.Sources) != 2 || a.Sources[0].Domain != "koala.com" || a.Sources[1].Title != "CHOICE" {
		t.Errorf("sources = %+v", a.Sources)
	}
	if a.SearchResults != 2 || len(a.FanOut) != 2 || len(a.BrandEntities) != 2 || a.Tag != "q1" || a.Cost != 0.004 {
		t.Errorf("answer = %+v", a)
	}
}

func TestGeminiOmitsForceWebSearch(t *testing.T) {
	var gotBody []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Write(fixture(t, "chatgpt_live.json"))
	}, budget.New(1, 0.01))
	if _, err := c.Live(context.Background(), Request{Engine: Gemini, Keyword: "x", ForceWebSearch: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody[0]["force_web_search"]; ok {
		t.Errorf("gemini body must not carry force_web_search: %v", gotBody[0])
	}
}

func TestAIOverviewReferencesAndAbsence(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture(t, "aio_live.json")) }, budget.New(1, 0.01))
	a, err := c.Live(context.Background(), Request{Engine: AIOverview, Keyword: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Present || a.Text == "" {
		t.Fatalf("expected an AI Overview, got %+v", a)
	}
	if len(a.Sources) != 2 {
		t.Errorf("want 2 deduplicated references from the AI Overview only, got %+v", a.Sources)
	}

	c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture(t, "aio_absent.json")) }, budget.New(1, 0.01))
	a, err = c.Live(context.Background(), Request{Engine: AIOverview, Keyword: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Present || len(a.Sources) != 0 {
		t.Errorf("expected no AI Overview, got %+v", a)
	}
}

func TestRetriesTransientThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write(fixture(t, "chatgpt_live.json"))
	}, budget.New(1, 0.01))
	if _, err := c.Live(context.Background(), Request{Engine: ChatGPT, Keyword: "x"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestDoesNotRetryPermanentErrors(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}, budget.New(1, 0.01))
	_, err := c.Live(context.Background(), Request{Engine: ChatGPT, Keyword: "x"})
	if err == nil || IsTransient(err) {
		t.Fatalf("want permanent error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestBudgetFailsClosedBeforeSending(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }, budget.New(0.01, 0.02))
	_, err := c.Live(context.Background(), Request{Engine: ChatGPT, Keyword: "x"})
	if !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("want budget.ErrExceeded, got %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("a refused call must not reach the API")
	}
}

func TestPostAndGetTask(t *testing.T) {
	var gets atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/task_post"):
			w.Write([]byte(`{"status_code":20000,"cost":0.0012,"tasks":[{"id":"t1","status_code":20100,"data":{"tag":"a"}},{"id":"t2","status_code":40501,"status_message":"bad field"}]}`))
		case strings.Contains(r.URL.Path, "/task_get/advanced/t1"):
			if gets.Add(1) == 1 {
				w.Write([]byte(`{"status_code":20000,"tasks":[{"id":"t1","status_code":40602,"status_message":"Task In Queue."}]}`))
				return
			}
			w.Write(fixture(t, "chatgpt_live.json"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}, budget.New(1, 0.001))

	posted, err := c.PostTasks(context.Background(), []Request{{Engine: ChatGPT, Keyword: "a", Tag: "a"}, {Engine: ChatGPT, Keyword: "b", Tag: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if posted[0].TaskID != "t1" || posted[0].Err != nil || posted[1].Err == nil {
		t.Fatalf("posted = %+v", posted)
	}
	if _, ready, err := c.GetTask(context.Background(), ChatGPT, "t1"); err != nil || ready {
		t.Fatalf("first get: ready=%v err=%v, want queued", ready, err)
	}
	a, ready, err := c.GetTask(context.Background(), ChatGPT, "t1")
	if err != nil || !ready || !a.Present {
		t.Fatalf("second get: ready=%v err=%v answer=%+v", ready, err, a)
	}
}

func TestParseEngine(t *testing.T) {
	if _, err := ParseEngine("perplexity"); err == nil {
		t.Error("perplexity is not a v1 engine")
	}
	if e, err := ParseEngine(" gemini "); err != nil || e != Gemini {
		t.Errorf("got %v %v", e, err)
	}
}

func TestEntityNames(t *testing.T) {
	if got := entityNames(json.RawMessage(`["A","B"]`)); len(got) != 2 {
		t.Errorf("strings: %v", got)
	}
	if got := entityNames(json.RawMessage(`[{"name":"A"},{"brand":"B"},{"x":1}]`)); len(got) != 2 {
		t.Errorf("objects: %v", got)
	}
	if got := entityNames(json.RawMessage(`null`)); got != nil {
		t.Errorf("null: %v", got)
	}
}
