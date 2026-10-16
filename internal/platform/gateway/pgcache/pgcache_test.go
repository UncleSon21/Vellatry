package pgcache_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/gateway/pgcache"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// provider stands in for the model vendor: the one thing these tests do not run for
// real. Each answer names the organisation and the call, so a cached answer can be told
// from a fresh one.
type provider struct {
	mu    sync.Mutex
	calls int
	down  bool
}

func (p *provider) Complete(_ context.Context, model string, _ int, req gateway.Request) (gateway.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.down {
		return gateway.Response{}, errors.New("provider: 529 overloaded")
	}
	return gateway.Response{Text: fmt.Sprintf("answer %d for %s", p.calls, req.OrgID), Model: model,
		Usage: gateway.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.02}}, nil
}

type noMeter struct{}

func (noMeter) Record(context.Context, gateway.MeterRecord) error { return nil }

func TestCache(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	orgA, _ := testdb.NewOrg(t, pool, "cache-a")
	orgB, _ := testdb.NewOrg(t, pool, "cache-b")
	c := pgcache.Cache{Pool: pool}
	stored := gateway.Response{Text: "Visibility fell on ChatGPT.", Model: "cheap-model",
		Usage: gateway.Usage{InputTokens: 120, OutputTokens: 30, CostUSD: 0.0021}}

	if _, hit, err := c.Get(ctx, orgA, "key-1"); hit || err != nil {
		t.Fatalf("an empty cache: hit %v, err %v; want a miss and no error", hit, err)
	}
	if err := c.Put(ctx, orgA, "agent_narrate", "key-1", stored); err != nil {
		t.Fatal(err)
	}
	got, hit, err := c.Get(ctx, orgA, "key-1")
	if err != nil || !hit || got != stored {
		t.Errorf("read back %+v, hit %v, err %v; want %+v", got, hit, err, stored)
	}

	// The key is a hash of the request, so two organisations asking the same thing share
	// it. The answer is still only the asker's.
	if got, hit, err := c.Get(ctx, orgB, "key-1"); hit || err != nil {
		t.Errorf("another organisation read the cached answer: %+v, hit %v, err %v", got, hit, err)
	}

	// Two workers can miss the cache together and both pay; the second to store must not
	// fail its job.
	if err := c.Put(ctx, orgA, "agent_narrate", "key-1", gateway.Response{Text: "a second answer", Model: "cheap-model"}); err != nil {
		t.Errorf("storing the same key twice: %v", err)
	}
	if _, hit, _ := c.Get(ctx, orgA, "key-1"); !hit {
		t.Error("the cached answer is gone after a second store")
	}
}

// TestPaidOnceAcrossRestarts runs the gateway as production wires it, on the Postgres
// cache, and restarts it: an identical request is never sent to the provider twice.
func TestPaidOnceAcrossRestarts(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	orgA, _ := testdb.NewOrg(t, pool, "paid-once-a")
	orgB, _ := testdb.NewOrg(t, pool, "paid-once-b")
	p := &provider{}
	start := func() *gateway.Gateway {
		g, err := gateway.New(gateway.Config{
			Purposes: gateway.DefaultPurposes(), Models: map[string]string{"cheap": "cheap-model", "strong": "strong-model"},
			Provider: p, Cache: pgcache.Cache{Pool: pool}, Budgets: &gateway.FixedBudgets{LimitUSD: 10, EstimateUSD: 0.02}, Meter: noMeter{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	ask := func(org, text string) gateway.Request {
		return gateway.Request{Purpose: "agent_narrate", OrgID: org, Messages: []gateway.Message{{Role: "user", Content: text}}}
	}
	question := "Why did visibility drop? " + time.Now().Format(time.RFC3339Nano)

	first, err := start().Complete(ctx, ask(orgA, question))
	if err != nil || first.Cached || p.calls != 1 {
		t.Fatalf("first ask: %+v, %v, %d calls", first, err, p.calls)
	}

	// A new process: nothing in memory, the same database.
	again, err := start().Complete(ctx, ask(orgA, question))
	if err != nil || p.calls != 1 {
		t.Fatalf("after a restart the provider was called again: %d calls, %v", p.calls, err)
	}
	if !again.Cached || again.Text != first.Text || again.Usage.CostUSD != 0 {
		t.Errorf("after a restart got %+v, want the first answer, marked cached, costing nothing", again)
	}

	// The same question from another organisation is that organisation's to pay for, and
	// its answer is its own.
	theirs, err := start().Complete(ctx, ask(orgB, question))
	if err != nil || p.calls != 2 || theirs.Cached || theirs.Text == first.Text {
		t.Errorf("another organisation asking the same: %+v, %v, %d calls; want its own paid answer", theirs, err, p.calls)
	}

	// A failed call stores nothing, so the retry asks again instead of finding a failure.
	g := start()
	retry := "Which competitor overtook us? " + time.Now().Format(time.RFC3339Nano)
	p.down = true
	if _, err := g.Complete(ctx, ask(orgA, retry)); err == nil {
		t.Fatal("a provider failure was not reported")
	}
	p.down = false
	if r, err := g.Complete(ctx, ask(orgA, retry)); err != nil || r.Cached || p.calls != 4 {
		t.Errorf("retry after a failure: %+v, %v, %d calls; want a fresh answer from the provider", r, err, p.calls)
	}
}
