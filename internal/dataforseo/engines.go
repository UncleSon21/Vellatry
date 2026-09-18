package dataforseo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Engine is an AI surface we measure.
type Engine string

const (
	ChatGPT    Engine = "chatgpt"
	Gemini     Engine = "gemini"
	AIOverview Engine = "ai_overview"
)

// ParseEngine validates an engine name.
func ParseEngine(s string) (Engine, error) {
	switch e := Engine(strings.TrimSpace(s)); e {
	case ChatGPT, Gemini, AIOverview:
		return e, nil
	}
	return "", fmt.Errorf("unknown engine %q (want chatgpt, gemini or ai_overview)", s)
}

type endpoints struct{ live, post, get string }

func (e Engine) endpoints() endpoints {
	switch e {
	case ChatGPT:
		return llmEndpoints("chat_gpt")
	case Gemini:
		return llmEndpoints("gemini")
	default:
		return endpoints{
			live: "/v3/serp/google/organic/live/advanced",
			post: "/v3/serp/google/organic/task_post",
			get:  "/v3/serp/google/organic/task_get/advanced/",
		}
	}
}

func llmEndpoints(name string) endpoints {
	p := "/v3/ai_optimization/" + name + "/llm_scraper/"
	return endpoints{live: p + "live/advanced", post: p + "task_post", get: p + "task_get/advanced/"}
}

// Request is one question to one engine.
type Request struct {
	Engine       Engine
	Keyword      string
	LocationCode int    // 2036 = Australia
	LanguageCode string // "en"
	// ForceWebSearch applies to ChatGPT only. Off matches real users: consumer ChatGPT
	// decides per question whether to search. Gemini rejects the parameter.
	ForceWebSearch bool
	// LoadAsyncAIOverview applies to AI Overview only; asks DataForSEO to wait for an
	// AI Overview that Google loads asynchronously. Verify against current docs.
	LoadAsyncAIOverview bool
	Tag                 string // echoed back by task_post/task_get to correlate results
}

func (r Request) body() map[string]any {
	b := map[string]any{
		"keyword":       r.Keyword,
		"location_code": r.LocationCode,
		"language_code": r.LanguageCode,
	}
	if r.Tag != "" {
		b["tag"] = r.Tag
	}
	switch r.Engine {
	case ChatGPT:
		b["force_web_search"] = r.ForceWebSearch
	case AIOverview:
		b["device"] = "desktop"
		b["depth"] = 10
		if r.LoadAsyncAIOverview {
			b["load_async_ai_overview"] = true
		}
	}
	return b
}

// Source is one citation shown with an answer.
type Source struct {
	URL    string `json:"url"`
	Title  string `json:"title,omitempty"`
	Domain string `json:"domain,omitempty"`
}

// Answer is the normalised result of one task.
type Answer struct {
	Engine        Engine   `json:"engine"`
	TaskID        string   `json:"task_id"`
	Tag           string   `json:"tag,omitempty"`
	Present       bool     `json:"present"`       // false when no AI answer was produced (e.g. no AI Overview on the page)
	AsyncPending  bool     `json:"async_pending"` // AI Overview flagged as loaded asynchronously and not captured
	Text          string   `json:"text"`
	Sources       []Source `json:"sources"`
	SearchResults int      `json:"search_results"`
	FanOut        []string `json:"fan_out,omitempty"`
	BrandEntities []string `json:"brand_entities,omitempty"`
	Model         string   `json:"model,omitempty"`
	Cost          float64  `json:"cost"`
}

// Live runs one request synchronously on the live endpoint (fast lane, higher price).
func (c *Client) Live(ctx context.Context, r Request) (Answer, error) {
	env, err := c.call(ctx, http.MethodPost, r.Engine.endpoints().live, []map[string]any{r.body()}, 1)
	if err != nil {
		return Answer{}, err
	}
	if len(env.Tasks) != 1 {
		return Answer{}, &APIError{Message: fmt.Sprintf("expected 1 task, got %d", len(env.Tasks))}
	}
	return parseTask(r.Engine, env.Tasks[0])
}

// Posted is the outcome of posting one task to the standard queue.
type Posted struct {
	TaskID string
	Tag    string
	Cost   float64 // charged at post time; task_get is free
	Err    error
}

// PostTasks queues up to 100 requests for one engine on the standard (cheaper) queue.
func (c *Client) PostTasks(ctx context.Context, reqs []Request) ([]Posted, error) {
	if len(reqs) == 0 || len(reqs) > 100 {
		return nil, fmt.Errorf("dataforseo: post 1 to 100 tasks, got %d", len(reqs))
	}
	engine := reqs[0].Engine
	bodies := make([]map[string]any, len(reqs))
	for i, r := range reqs {
		if r.Engine != engine {
			return nil, fmt.Errorf("dataforseo: one engine per post, got %s and %s", engine, r.Engine)
		}
		bodies[i] = r.body()
	}
	env, err := c.call(ctx, http.MethodPost, engine.endpoints().post, bodies, len(reqs))
	if err != nil {
		return nil, err
	}
	out := make([]Posted, len(reqs))
	for i := range reqs {
		out[i].Tag = reqs[i].Tag
		if i >= len(env.Tasks) {
			out[i].Err = &APIError{Message: "task missing from response"}
			continue
		}
		t := env.Tasks[i]
		out[i].TaskID = t.ID
		out[i].Cost = t.Cost
		if t.StatusCode != 20100 && t.StatusCode != 20000 {
			out[i].Err = &APIError{StatusCode: t.StatusCode, Message: t.StatusMessage, Transient: t.StatusCode >= 50000}
		}
	}
	return out, nil
}

// GetTask fetches a queued task. ready is false while it is still queued or running.
func (c *Client) GetTask(ctx context.Context, e Engine, taskID string) (a Answer, ready bool, err error) {
	env, err := c.call(ctx, http.MethodGet, e.endpoints().get+url.PathEscape(taskID), nil, 0)
	if err != nil {
		return Answer{}, false, err
	}
	if len(env.Tasks) != 1 {
		return Answer{}, false, &APIError{Message: fmt.Sprintf("expected 1 task, got %d", len(env.Tasks))}
	}
	switch env.Tasks[0].StatusCode {
	case 40601, 40602: // handed to a worker / still in queue
		return Answer{}, false, nil
	}
	a, err = parseTask(e, env.Tasks[0])
	return a, err == nil, err
}

func parseTask(e Engine, t task) (Answer, error) {
	if t.StatusCode != 20000 {
		return Answer{}, &APIError{StatusCode: t.StatusCode, Message: t.StatusMessage, Transient: t.StatusCode >= 50000}
	}
	a := Answer{Engine: e, TaskID: t.ID, Cost: t.Cost}
	var data struct {
		Tag string `json:"tag"`
	}
	_ = json.Unmarshal(t.Data, &data)
	a.Tag = data.Tag

	var results []json.RawMessage
	if len(t.Result) > 0 && string(t.Result) != "null" {
		if err := json.Unmarshal(t.Result, &results); err != nil {
			return a, &APIError{Message: "decode result: " + err.Error()}
		}
	}
	if len(results) == 0 {
		return a, nil
	}
	var err error
	if e == AIOverview {
		err = parseSERP(results[0], &a)
	} else {
		err = parseLLM(results[0], &a)
	}
	return a, err
}

func parseLLM(raw json.RawMessage, a *Answer) error {
	var r struct {
		Model    string `json:"model"`
		Markdown string `json:"markdown"`
		Items    []struct {
			Markdown string `json:"markdown"`
			Text     string `json:"text"`
			Sections []struct {
				Markdown string `json:"markdown"`
				Text     string `json:"text"`
			} `json:"sections"`
		} `json:"items"`
		Sources []struct {
			URL    string `json:"url"`
			Title  string `json:"title"`
			Source string `json:"source"`
			Domain string `json:"domain"`
		} `json:"sources"`
		SearchResults []json.RawMessage `json:"search_results"`
		FanOut        []string          `json:"fan_out_queries"`
		BrandEntities json.RawMessage   `json:"brand_entities"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return &APIError{Message: "decode llm result: " + err.Error()}
	}
	text := strings.TrimSpace(r.Markdown)
	if text == "" {
		var parts []string
		for _, it := range r.Items {
			if it.Markdown != "" {
				parts = append(parts, it.Markdown)
				continue
			}
			for _, s := range it.Sections {
				if p := firstNonEmpty(s.Markdown, s.Text); p != "" {
					parts = append(parts, p)
				}
			}
			if len(it.Sections) == 0 && it.Text != "" {
				parts = append(parts, it.Text)
			}
		}
		text = strings.TrimSpace(strings.Join(parts, "\n\n"))
	}
	a.Model = r.Model
	a.Text = text
	a.Present = text != ""
	seen := map[string]bool{}
	for _, s := range r.Sources {
		addSource(a, seen, s.URL, firstNonEmpty(s.Title, s.Source), s.Domain)
	}
	a.SearchResults = len(r.SearchResults)
	a.FanOut = r.FanOut
	a.BrandEntities = entityNames(r.BrandEntities)
	return nil
}

// parseSERP extracts the AI Overview block(s) from an organic SERP result. Citations are
// harvested from every "references" array under the AI Overview items, however nested.
func parseSERP(raw json.RawMessage, a *Answer) error {
	var r struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return &APIError{Message: "decode serp result: " + err.Error()}
	}
	seen := map[string]bool{}
	var parts []string
	for _, it := range r.Items {
		if it["type"] != "ai_overview" {
			continue
		}
		a.Present = true
		if v, _ := it["asynchronous_ai_overview"].(bool); v {
			a.AsyncPending = true
		}
		if md, _ := it["markdown"].(string); strings.TrimSpace(md) != "" {
			parts = append(parts, strings.TrimSpace(md))
		} else if tx, _ := it["text"].(string); strings.TrimSpace(tx) != "" {
			parts = append(parts, strings.TrimSpace(tx))
		}
		walkReferences(it, func(u, title, domain string) { addSource(a, seen, u, title, domain) })
	}
	a.Text = strings.Join(parts, "\n\n")
	if a.Text != "" {
		a.AsyncPending = false
	}
	return nil
}

func walkReferences(v any, add func(u, title, domain string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if k == "references" {
				if refs, ok := child.([]any); ok {
					for _, ref := range refs {
						if m, ok := ref.(map[string]any); ok {
							u, _ := m["url"].(string)
							t, _ := m["title"].(string)
							s, _ := m["source"].(string)
							d, _ := m["domain"].(string)
							add(u, firstNonEmpty(t, s), d)
						}
					}
				}
				continue
			}
			walkReferences(child, add)
		}
	case []any:
		for _, child := range x {
			walkReferences(child, add)
		}
	}
}

func addSource(a *Answer, seen map[string]bool, rawURL, title, domain string) {
	u := strings.TrimSpace(rawURL)
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	if u == "" || seen[u] {
		return
	}
	seen[u] = true
	if domain == "" {
		if p, err := url.Parse(u); err == nil {
			domain = strings.TrimPrefix(strings.ToLower(p.Hostname()), "www.")
		}
	}
	a.Sources = append(a.Sources, Source{URL: u, Title: title, Domain: domain})
}

// entityNames reads brand_entities leniently: an array of strings, or of objects with
// a title/name/brand field. The exact shape is confirmed by spike 1's first live run.
func entityNames(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var strs []string
	if json.Unmarshal(raw, &strs) == nil {
		return strs
	}
	var objs []map[string]any
	if json.Unmarshal(raw, &objs) != nil {
		return nil
	}
	var names []string
	for _, o := range objs {
		for _, k := range []string{"title", "name", "brand"} {
			if s, ok := o[k].(string); ok && s != "" {
				names = append(names, s)
				break
			}
		}
	}
	return names
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
