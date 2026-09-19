// Package pgcache stores gateway responses in Postgres, per tenant, so an identical
// request is never paid for twice, across processes and restarts.
package pgcache

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/platform/gateway"
)

// Cache implements gateway.Cache on the llm_cache table.
type Cache struct {
	Pool *pgxpool.Pool
}

// Get implements gateway.Cache.
func (c Cache) Get(ctx context.Context, orgID, key string) (gateway.Response, bool, error) {
	var raw []byte
	err := db.InTenant(ctx, c.Pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT response FROM llm_cache WHERE org_id = $1 AND key = $2", orgID, key).Scan(&raw)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gateway.Response{}, false, nil
	}
	if err != nil {
		return gateway.Response{}, false, err
	}
	var r gateway.Response
	if err := json.Unmarshal(raw, &r); err != nil {
		return gateway.Response{}, false, err
	}
	return r, true, nil
}

// Put implements gateway.Cache.
func (c Cache) Put(ctx context.Context, orgID, purpose, key string, r gateway.Response) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return db.InTenant(ctx, c.Pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO llm_cache (org_id, key, purpose, model, response) VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (org_id, key) DO NOTHING`,
			orgID, key, purpose, r.Model, raw)
		return err
	})
}
