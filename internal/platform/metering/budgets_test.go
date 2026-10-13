package metering_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
	"github.com/UncleSon21/vellatry/internal/platform/gateway/pgcache"
	"github.com/UncleSon21/vellatry/internal/platform/metering"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// spend writes one ledger row. at is a SQL expression for when it happened.
func spend(t *testing.T, ctx context.Context, tx pgx.Tx, org, at, provider string, usd float64) {
	t.Helper()
	_, err := tx.Exec(ctx, `INSERT INTO usage_ledger (org_id, occurred_at, provider, purpose, cost_usd)
		VALUES ($1, `+at+`, $2, 'agent_narrate', $3)`, org, provider, usd)
	if err != nil {
		t.Fatal(err)
	}
}

// The day's budget starts from what the ledger already shows: today, in UTC, for this
// organisation and this provider, and nothing else.
func TestDailyBudgetCountsOnlyTodaysOwnSpend(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "budget-own")
	other, _ := testdb.NewOrg(t, pool, "budget-other")
	const now, yesterday = "now()", "date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 hour'"
	if err := db.InSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		spend(t, ctx, tx, org, now, "anthropic", 0.30)       // counts
		spend(t, ctx, tx, org, yesterday, "anthropic", 5.00) // yesterday's
		spend(t, ctx, tx, org, now, "dataforseo", 5.00)      // another provider's
		spend(t, ctx, tx, other, now, "anthropic", 5.00)     // another organisation's
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	d := &metering.DailyBudgets{Pool: pool, Provider: "anthropic", LimitUSD: 1, EstimateUSD: 0.25}
	b := d.For(org)
	// $0.70 is left: two calls at $0.25 fit, three do not.
	release, err := b.Reserve(2)
	if err != nil {
		t.Fatalf("$0.50 of a $0.70 remainder was refused (did spend that is not today's own count?): %v", err)
	}
	release(0)
	if _, err := b.Reserve(3); !errors.Is(err, budget.ErrExceeded) {
		t.Errorf("$0.75 of a $0.70 remainder: %v, want ErrExceeded (did today's $0.30 not count?)", err)
	}

	// An organisation already past its cap is refused before anything is sent.
	if _, err := d.For(other).Reserve(1); !errors.Is(err, budget.ErrExceeded) {
		t.Errorf("an organisation $5 into a $1 day: %v, want ErrExceeded", err)
	}
}

// When the ledger cannot be read the budget is zero, and that refusal is not kept: the
// next call reads the ledger again.
func TestDailyBudgetFailsClosed(t *testing.T) {
	pool := testdb.Pool(t)
	org, _ := testdb.NewOrg(t, pool, "budget-closed")
	down := testdb.Pool(t)
	down.Close() // a database that cannot be reached

	d := &metering.DailyBudgets{Pool: down, Provider: "anthropic", LimitUSD: 100, EstimateUSD: 0.01}
	if _, err := d.For(org).Reserve(1); !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("with the ledger unreadable a paid call got %v, want ErrExceeded", err)
	}

	d.Pool = pool // the database is back
	release, err := d.For(org).Reserve(1)
	if err != nil {
		t.Fatalf("once the ledger is readable again the budget is still refused: %v", err)
	}
	release(0)
}

// costly stands in for the model vendor, at $0.40 a call.
type costly struct {
	mu    sync.Mutex
	calls int
}

func (p *costly) Complete(_ context.Context, model string, _ int, _ gateway.Request) (gateway.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return gateway.Response{Text: fmt.Sprintf("answer %d", p.calls), Model: model,
		Usage: gateway.Usage{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.40}}, nil
}

// TestSpendSurvivesARestart wires the gateway as production does: the Postgres cache, the
// daily budget and the meter writing the ledger. What one process spent, the next one
// knows about, and a cached answer is free even on a day that has run out.
func TestSpendSurvivesARestart(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "budget-restart")
	p := &costly{}
	start := func() *gateway.Gateway {
		g, err := gateway.New(gateway.Config{
			Purposes: gateway.DefaultPurposes(), Models: map[string]string{"cheap": "cheap-model", "strong": "strong-model"},
			Provider: p, Cache: pgcache.Cache{Pool: pool},
			Budgets: &metering.DailyBudgets{Pool: pool, Provider: "anthropic", LimitUSD: 1, EstimateUSD: 0.40},
			Meter:   metering.GatewayMeter{Pool: pool, Provider: "anthropic"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	ask := func(text string) gateway.Request {
		return gateway.Request{Purpose: "agent_narrate", OrgID: org, Messages: []gateway.Message{{Role: "user", Content: text + " " + org}}}
	}

	g := start()
	for _, q := range []string{"first", "second"} {
		if _, err := g.Complete(ctx, ask(q)); err != nil {
			t.Fatalf("%s call within a $1 day: %v", q, err)
		}
	}
	if _, err := g.Complete(ctx, ask("third")); !errors.Is(err, budget.ErrExceeded) || p.calls != 2 {
		t.Fatalf("a third $0.40 call on $0.80 spent: %v, %d provider calls; want ErrExceeded before sending", err, p.calls)
	}

	restarted := start()
	if _, err := restarted.Complete(ctx, ask("fourth")); !errors.Is(err, budget.ErrExceeded) || p.calls != 2 {
		t.Errorf("after a restart: %v, %d provider calls; the ledger's $0.80 should still count", err, p.calls)
	}
	r, err := restarted.Complete(ctx, ask("first"))
	if err != nil || !r.Cached || p.calls != 2 {
		t.Errorf("a cached answer on a spent day: %+v, %v, %d provider calls; want it served from the cache for nothing", r, err, p.calls)
	}

	var paid float64
	var cached int
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(sum(cost_usd), 0)::float8, count(*) FILTER (WHERE cached)
			FROM usage_ledger WHERE org_id = $1 AND provider = 'anthropic'`, org).Scan(&paid, &cached)
	}); err != nil {
		t.Fatal(err)
	}
	if paid < 0.79 || paid > 0.81 || cached != 1 {
		t.Errorf("ledger shows $%.2f paid and %d cached answers; want $0.80 and 1", paid, cached)
	}
}
