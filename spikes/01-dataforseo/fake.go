package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"
)

// fakeTransport imitates DataForSEO for -dry-run: plausible answers mentioning random
// configured brands, costs that are not real, and a queue that needs one poll.
type fakeTransport struct {
	mu      sync.Mutex
	rng     *rand.Rand
	names   []string
	domains []string
	tasks   map[string]*fakeTask
	next    int
}

type fakeTask struct {
	engine  string
	tag     string
	keyword string
	polls   int
}

func newFakeTransport(cfg config) *fakeTransport {
	f := &fakeTransport{rng: rand.New(rand.NewPCG(1, 2)), tasks: map[string]*fakeTask{}}
	for _, s := range cfg.Sets {
		f.names = append(f.names, s.Brand.Name)
		f.domains = append(f.domains, s.Brand.Domains...)
		for _, c := range s.Competitors {
			f.names = append(f.names, c.Name)
			f.domains = append(f.domains, c.Domains...)
		}
	}
	f.domains = append(f.domains, "choice.com.au", "reddit.com", "canstar.com.au")
	return f
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	engine := "ai_overview"
	switch {
	case strings.Contains(path, "/chat_gpt/"):
		engine = "chatgpt"
	case strings.Contains(path, "/gemini/"):
		engine = "gemini"
	}
	var bodies []map[string]any
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(b, &bodies)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	var env map[string]any
	switch {
	case strings.HasSuffix(path, "/live/advanced"):
		time.Sleep(time.Duration(20+f.rng.IntN(60)) * time.Millisecond)
		t := &fakeTask{engine: engine, tag: str(bodies[0]["tag"]), keyword: str(bodies[0]["keyword"])}
		task := f.result(f.newID(), t, true)
		env = envelope(task["cost"].(float64), task)
	case strings.HasSuffix(path, "/task_post"):
		var tasks []any
		total := 0.0
		for _, b := range bodies {
			id := f.newID()
			f.tasks[id] = &fakeTask{engine: engine, tag: str(b["tag"]), keyword: str(b["keyword"])}
			cost := unitCost(engine, false)
			total += cost
			tasks = append(tasks, map[string]any{"id": id, "status_code": 20100, "status_message": "Task Created.", "cost": cost, "data": map[string]any{"tag": str(b["tag"])}})
		}
		env = envelope(total, tasks...)
	case strings.Contains(path, "/task_get/advanced/"):
		id := path[strings.LastIndex(path, "/")+1:]
		t, ok := f.tasks[id]
		if !ok {
			env = envelope(0, map[string]any{"id": id, "status_code": 40400, "status_message": "Not Found."})
			break
		}
		t.polls++
		if t.polls < 2 {
			env = envelope(0, map[string]any{"id": id, "status_code": 40602, "status_message": "Task In Queue."})
			break
		}
		task := f.result(id, t, false)
		task["cost"] = 0.0
		env = envelope(0, task)
	default:
		return nil, fmt.Errorf("fake: unexpected path %s", path)
	}
	b, _ := json.Marshal(env)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{}, Request: req}, nil
}

func (f *fakeTransport) newID() string {
	f.next++
	return fmt.Sprintf("fake-%d", f.next)
}

func (f *fakeTransport) result(id string, t *fakeTask, live bool) map[string]any {
	var mentioned []string
	for _, n := range f.names {
		if f.rng.Float64() < 0.3 {
			mentioned = append(mentioned, n)
		}
	}
	text := "For " + t.keyword + ", buyers usually compare a few options."
	if len(mentioned) > 0 {
		text += " Popular choices include " + strings.Join(mentioned, ", ") + "."
	}
	var sources []any
	for i := 0; i < f.rng.IntN(4); i++ {
		d := f.domains[f.rng.IntN(len(f.domains))]
		sources = append(sources, map[string]any{"url": fmt.Sprintf("https://www.%s/page-%d", d, i), "title": d})
	}
	var result map[string]any
	if t.engine == "ai_overview" {
		items := []any{map[string]any{"type": "organic", "url": "https://example.com"}}
		if f.rng.Float64() < 0.7 {
			items = append(items, map[string]any{"type": "ai_overview", "markdown": text, "references": sources})
		}
		result = map[string]any{"items": items}
	} else {
		var entities []any
		for _, m := range mentioned {
			entities = append(entities, map[string]any{"title": m})
		}
		result = map[string]any{
			"model":           "fake",
			"markdown":        text,
			"sources":         sources,
			"search_results":  sources,
			"fan_out_queries": []string{t.keyword + " reviews", t.keyword + " 2026"},
			"brand_entities":  entities,
		}
	}
	return map[string]any{
		"id": id, "status_code": 20000, "status_message": "Ok.", "cost": unitCost(t.engine, live),
		"data":   map[string]any{"tag": t.tag, "keyword": t.keyword},
		"result": []any{result},
	}
}

func unitCost(engine string, live bool) float64 {
	c := 0.002
	if engine != "ai_overview" {
		c = 0.004
	}
	if !live {
		c /= 3
	}
	return c
}

func envelope(cost float64, tasks ...any) map[string]any {
	return map[string]any{"status_code": 20000, "status_message": "Ok.", "cost": cost, "tasks": tasks}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
