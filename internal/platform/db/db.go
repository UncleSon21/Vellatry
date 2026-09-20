// Package db opens Postgres, runs migrations, and scopes every transaction to a role.
//
// Row-level security is forced on every tenant table, so a transaction must say whose
// data it acts on:
//
//   - InTenant: one org's rows only (the normal case).
//   - InSystem: cross-tenant work such as the scheduler or signup. Explicit and greppable.
//
// A query that picks neither runs as the connection owner, which matches no policy and
// sees no rows: forgetting fails closed.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

const (
	roleTenant = "vellatry_tenant"
	roleSystem = "vellatry_system"
)

// Open connects a pool and checks it is reachable.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// migrationLock is an arbitrary but fixed key: every Vellatry process waits on the
// same one.
const migrationLock int64 = 8314552901001

// Migrate applies River's schema, then Vellatry's. Safe to run repeatedly, and safe to
// run from several processes at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// One migration at a time across every process. Two api or worker instances can
	// start together, and `go test ./...` runs each package in its own process against
	// the same test database; without this they race, and one fails on a table the
	// other has just created.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return fmt.Errorf("db: waiting for the migration lock: %w", err)
	}
	defer func() {
		// Best effort: the lock is released with the connection in any case.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLock)
	}()

	rm, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	if _, err := rm.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("db: river migrations: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("db: migrations: %w", err)
	}
	return nil
}

// InTenant runs fn in a transaction that can see and write only orgID's rows.
func InTenant(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if !isUUID(orgID) {
		return fmt.Errorf("db: tenant transaction needs an org uuid, got %q", orgID)
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+roleTenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// InSystem runs fn in a cross-tenant transaction. Use only for work that genuinely spans
// tenants (scheduling, signup, admin); tenant work uses InTenant.
func InSystem(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+roleSystem); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// AsOwner runs fn with the connection's own role inside a tenant or system transaction,
// then restores the previous role. It exists only to write to infrastructure tables that
// have no row-level security, such as the job queue, in the same transaction as tenant
// data. Never use it to read or write tenant tables.
func AsOwner(ctx context.Context, tx pgx.Tx, fn func() error) error {
	var prev string
	if err := tx.QueryRow(ctx, "SELECT current_user").Scan(&prev); err != nil {
		return err
	}
	if prev != roleTenant && prev != roleSystem {
		return errors.New("db: AsOwner called outside InTenant or InSystem")
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE NONE"); err != nil {
		return err
	}
	fnErr := fn()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+prev); err != nil {
		return errors.Join(fnErr, err)
	}
	return fnErr
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}
