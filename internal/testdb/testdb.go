// Package testdb gives integration tests a migrated database. Tests that use it are
// skipped unless VELLATRY_TEST_DATABASE_URL is set (see deploy/docker-compose.yml).
package testdb

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UncleSon21/vellatry/internal/platform/db"
)

var (
	migrateOnce sync.Once
	migrateErr  error
)

// Pool returns a pool on the test database, migrated once per test binary.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("VELLATRY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set VELLATRY_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var bypass bool
	if err := pool.QueryRow(ctx, "SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&bypass); err != nil {
		t.Fatal(err)
	}
	if bypass {
		t.Fatal("test database user bypasses row-level security; connect as a non-superuser such as vellatry_app")
	}

	migrateOnce.Do(func() { migrateErr = db.Migrate(ctx, pool) })
	if migrateErr != nil {
		t.Fatal(migrateErr)
	}
	return pool
}

// NewOrg creates an org with one brand through a system transaction.
func NewOrg(t testing.TB, pool *pgxpool.Pool, name string) (orgID, brandID string) {
	t.Helper()
	domain := fmt.Sprintf("%s-%d.example", name, time.Now().UnixNano())
	err := db.InSystem(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "INSERT INTO orgs (name) VALUES ($1) RETURNING id::text", name).Scan(&orgID); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			"INSERT INTO brands (org_id, name, domain) VALUES ($1, $2, $3) RETURNING id::text",
			orgID, name, domain).Scan(&brandID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return orgID, brandID
}
