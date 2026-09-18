package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// TestTenantIsolation proves one company can never read or write another's rows,
// enforced by the database rather than by application code.
func TestTenantIsolation(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	orgA, brandA := testdb.NewOrg(t, pool, "a")
	orgB, brandB := testdb.NewOrg(t, pool, "b")

	t.Run("reads only its own rows", func(t *testing.T) {
		seen := map[string]bool{}
		err := db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, "SELECT id::text FROM brands")
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			for _, id := range ids {
				seen[id] = true
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !seen[brandA] || seen[brandB] || len(seen) != 1 {
			t.Errorf("org A saw %v; want only %s", seen, brandA)
		}
	})

	t.Run("cannot insert into another org", func(t *testing.T) {
		err := db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO brands (org_id, name, domain) VALUES ($1, 'x', 'x.example')", orgB)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("want row-level security violation (42501), got %v", err)
		}
	})

	t.Run("cannot update or delete another org's rows", func(t *testing.T) {
		err := db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
			up, err := tx.Exec(ctx, "UPDATE brands SET name = 'hacked' WHERE id = $1", brandB)
			if err != nil {
				return err
			}
			del, err := tx.Exec(ctx, "DELETE FROM brands WHERE id = $1", brandB)
			if err != nil {
				return err
			}
			if up.RowsAffected() != 0 || del.RowsAffected() != 0 {
				t.Errorf("affected rows: update %d, delete %d; want 0", up.RowsAffected(), del.RowsAffected())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("sees only its own org record", func(t *testing.T) {
		var mine, theirs int
		err := db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM orgs WHERE id = $1", orgA).Scan(&mine); err != nil {
				return err
			}
			return tx.QueryRow(ctx, "SELECT count(*) FROM orgs WHERE id = $1", orgB).Scan(&theirs)
		})
		if err != nil {
			t.Fatal(err)
		}
		if mine != 1 || theirs != 0 {
			t.Errorf("own org %d, other org %d; want 1 and 0", mine, theirs)
		}
	})

	t.Run("no role chosen sees nothing", func(t *testing.T) {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM brands WHERE id = ANY($1)", []string{brandA, brandB}).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("a query outside InTenant/InSystem saw %d rows; want 0", n)
		}
	})

	t.Run("tenant role without an org sees nothing", func(t *testing.T) {
		var n int
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL ROLE vellatry_tenant"); err != nil {
				return err
			}
			return tx.QueryRow(ctx, "SELECT count(*) FROM brands").Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("tenant role with no org saw %d rows; want 0", n)
		}
	})

	t.Run("rejects a malformed org id", func(t *testing.T) {
		err := db.InTenant(ctx, pool, "not-a-uuid' OR true --", func(context.Context, pgx.Tx) error { return nil })
		if err == nil {
			t.Error("malformed org id accepted")
		}
	})

	t.Run("system transactions see every tenant", func(t *testing.T) {
		var n int
		err := db.InSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM brands WHERE id = ANY($1)", []string{brandA, brandB}).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("system saw %d of 2 brands", n)
		}
	})
}

func TestAsOwnerRestoresTheTenantRole(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	orgA, _ := testdb.NewOrg(t, pool, "owner")
	err := db.InTenant(ctx, pool, orgA, func(ctx context.Context, tx pgx.Tx) error {
		var inside, after string
		if err := db.AsOwner(ctx, tx, func() error {
			return tx.QueryRow(ctx, "SELECT current_user").Scan(&inside)
		}); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT current_user").Scan(&after); err != nil {
			return err
		}
		if inside == "vellatry_tenant" || after != "vellatry_tenant" {
			t.Errorf("inside AsOwner %q, after %q; want owner then vellatry_tenant", inside, after)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return db.AsOwner(ctx, tx, func() error { return nil })
	}); err == nil {
		t.Error("AsOwner outside a scoped transaction should be refused")
	}
}
