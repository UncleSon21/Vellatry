package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
)

type fakeProvider struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeProvider) Complete(_ context.Context, model string, _ int, req Request) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return Response{Text: "ok from " + model, Usage: Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.001}}, nil
}

type fakeMeter struct{ records []MeterRecord }

func (m *fakeMeter) Record(_ context.Context, r MeterRecord) error {
	m.records = append(m.records, r)
	return nil
}

var models = map[string]string{"cheap": "cheap-model", "strong": "strong-model"}

func newGateway(t *testing.T, allow []string, limitUSD float64) (*Gateway, *fakeProvider, *fakeMeter) {
	t.Helper()
	p, m := &fakeProvider{}, &fakeMeter{}
	g, err := New(Config{
		Purposes: DefaultPurposes(), AllowPerItem: allow, Models: models,
		Provider: p, Cache: NewMemoryCache(), Budgets: &FixedBudgets{LimitUSD: limitUSD, EstimateUSD: 0.01}, Meter: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, p, m
}

func ask(purpose, org, text string) Request {
	return Request{Purpose: purpose, OrgID: org, Messages: []Message{{Role: "user", Content: text}}}
}

func TestRefusalsNeverReachTheProvider(t *testing.T) {
	g, p, _ := newGateway(t, nil, 10)
	cases := []struct {
		name string
		req  Request
		want error
	}{
		{"unknown purpose", ask("summarise_everything", "org", "hi"), ErrUnknownPurpose},
		{"per-item not allowed", ask("judge", "org", "hi"), ErrPerItemRefused},
		{"no org", ask("agent_narrate", "", "hi"), ErrNoOrg},
		{"too large", ask("suggest_title_meta", "org", strings.Repeat("x", 6_001)), ErrPromptTooLarge},
	}
	for _, c := range cases {
		if _, err := g.Complete(context.Background(), c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	if p.calls != 0 {
		t.Errorf("provider called %d times for refused requests", p.calls)
	}
}

func TestPerItemAllowedOnlyWhenListed(t *testing.T) {
	g, p, _ := newGateway(t, []string{"judge"}, 10)
	if _, err := g.Complete(context.Background(), ask("judge", "org", "hi")); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Errorf("calls = %d", p.calls)
	}
}

func TestIdenticalRequestsArePaidOnce(t *testing.T) {
	g, p, m := newGateway(t, nil, 10)
	first, err := g.Complete(context.Background(), ask("agent_narrate", "org", "explain"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.Complete(context.Background(), ask("agent_narrate", "org", "explain"))
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Errorf("provider calls = %d, want 1", p.calls)
	}
	if first.Cached || !second.Cached || second.Usage.CostUSD != 0 || second.Text != first.Text {
		t.Errorf("first %+v, second %+v", first, second)
	}
	if len(m.records) != 2 || !m.records[1].Cached || m.records[0].Usage.CostUSD != 0.001 {
		t.Errorf("meter records = %+v", m.records)
	}
	if first.Model != "cheap-model" {
		t.Errorf("model = %s", first.Model)
	}
}

func TestBudgetFailsClosedPerTenant(t *testing.T) {
	g, p, _ := newGateway(t, nil, 0.005) // below agent_narrate's reservation of 0.01
	if _, err := g.Complete(context.Background(), ask("agent_narrate", "org-a", "x")); !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("want budget.ErrExceeded, got %v", err)
	}
	if p.calls != 0 {
		t.Errorf("provider was called despite the budget")
	}
}

func TestNewValidatesConfig(t *testing.T) {
	base := Config{Models: models, Provider: &fakeProvider{}, Cache: NewMemoryCache(), Budgets: &FixedBudgets{LimitUSD: 1, EstimateUSD: 0.01}, Meter: &fakeMeter{}}
	bad := []Config{
		func() Config {
			c := base
			c.Purposes = DefaultPurposes()
			c.AllowPerItem = []string{"agent_plan"}
			return c
		}(),
		func() Config {
			c := base
			c.Purposes = []Purpose{{Name: "x", MaxInputChars: 1, MaxOutputTokens: 1, EstimateUSD: 1, Tier: "missing"}}
			return c
		}(),
		func() Config { c := base; c.Purposes = []Purpose{{Name: "x", Tier: "cheap"}}; return c }(),
		func() Config { c := base; c.Purposes = append(DefaultPurposes(), DefaultPurposes()[0]); return c }(),
	}
	for i, c := range bad {
		if _, err := New(c); err == nil {
			t.Errorf("config %d should be rejected", i)
		}
	}
}
