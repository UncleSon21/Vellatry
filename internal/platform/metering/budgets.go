package metering

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/db"
)

// DailyBudgets caps each tenant's spend with one provider per UTC day. Today's budget
// starts from what the ledger already shows as spent, so restarts and other processes
// are accounted for. If the ledger cannot be read the budget is zero: fail closed.
type DailyBudgets struct {
	Pool        *pgxpool.Pool
	Provider    string  // ledger provider to count, e.g. "anthropic"
	LimitUSD    float64 // per tenant per day
	EstimateUSD float64 // per-call reservation until real costs are seen
	Logger      *slog.Logger

	mu sync.Mutex
	m  map[string]dailyEntry
}

type dailyEntry struct {
	day string
	b   *budget.Budget
}

// For implements gateway.Budgets.
func (d *DailyBudgets) For(orgID string) *budget.Budget {
	today := time.Now().UTC().Format(time.DateOnly)
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.m[orgID]; ok && e.day == today {
		return e.b
	}
	remaining := 0.0
	spent, err := d.spentToday(orgID)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Error("daily budget: ledger unreadable, refusing paid calls", "org_id", orgID, "error", err)
		}
	} else if spent < d.LimitUSD {
		remaining = d.LimitUSD - spent
	}
	b := budget.New(remaining, d.EstimateUSD)
	if err == nil { // never cache a fail-closed budget; retry the ledger next call
		if d.m == nil {
			d.m = map[string]dailyEntry{}
		}
		d.m[orgID] = dailyEntry{day: today, b: b}
	}
	return b
}

func (d *DailyBudgets) spentToday(orgID string) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var spent float64
	err := db.InTenant(ctx, d.Pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT coalesce(sum(cost_usd), 0)::float8 FROM usage_ledger
			 WHERE org_id = $1 AND provider = $2 AND occurred_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`,
			orgID, d.Provider).Scan(&spent)
	})
	return spent, err
}
