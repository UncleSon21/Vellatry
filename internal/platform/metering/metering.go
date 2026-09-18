// Package metering writes every paid external call to the tenant's usage ledger.
package metering

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
)

// Usage is one paid call.
type Usage struct {
	Provider string  // "dataforseo", "anthropic", ...
	Purpose  string  // gateway purpose or engine
	Units    float64 // tasks, or tokens
	CostUSD  float64
	Cached   bool
	Ref      string // task id or request hash
}

// Record inserts u for orgID inside tx (a db.InTenant transaction for that org).
func Record(ctx context.Context, tx pgx.Tx, orgID string, u Usage) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO usage_ledger (org_id, provider, purpose, units, cost_usd, cached, ref)
		 VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''))`,
		orgID, u.Provider, u.Purpose, u.Units, u.CostUSD, u.Cached, u.Ref)
	return err
}

// GatewayMeter records LLM gateway calls in the ledger.
type GatewayMeter struct {
	Pool     *pgxpool.Pool
	Provider string
}

// Record implements gateway.Meter.
func (m GatewayMeter) Record(ctx context.Context, r gateway.MeterRecord) error {
	return db.InTenant(ctx, m.Pool, r.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		return Record(ctx, tx, r.OrgID, Usage{
			Provider: m.Provider,
			Purpose:  r.Purpose,
			Units:    float64(r.Usage.InputTokens + r.Usage.OutputTokens),
			CostUSD:  r.Usage.CostUSD,
			Cached:   r.Cached,
			Ref:      r.Model,
		})
	})
}
