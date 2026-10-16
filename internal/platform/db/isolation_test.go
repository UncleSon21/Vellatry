package db_test

import (
	"context"
	"errors"
	"strings"
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

// orgRule is the policy that keeps a table with an org_id column to one tenant.
// TestTenantIsolation shows it working on brands; TestEveryTableIsIsolated shows every
// such table uses it.
const orgRule = "(org_id = app_org_id())"

// ownRule is the policy of each tenant-readable table that has no org_id column.
var ownRule = map[string]string{
	"orgs":  "(id = app_org_id())",
	"users": "(EXISTS ( SELECT 1 FROM memberships m WHERE ((m.user_id = users.id) AND (m.org_id = app_org_id()))))",
}

// sharedTables hold no tenant's data, and the tenant role has no privilege on them at all.
var sharedTables = map[string]string{
	"goose_db_version":   "migration bookkeeping",
	"river_job":          "River's queue: only the owner writes it, through db.AsOwner inside a tenant transaction",
	"river_leader":       "River's leader election",
	"river_migration":    "River's own migrations",
	"river_notification": "River's notifications",
	"river_queue":        "River's queues",
	"worker_halt":        "the halt switch for Vellatry's own provider accounts, never a tenant's",
}

// TestEveryTableIsIsolated reads the database's own catalog, because each migration
// turns row-level security on for a list of tables it names, and a table left off the
// list would be readable by every tenant with nothing else failing. Every table is
// either kept to one tenant by a forced policy, or listed in sharedTables with the reason
// and out of the tenant role's reach.
func TestEveryTableIsIsolated(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()

	type table struct {
		rls, forced, orgID, tenantCanUse bool
	}
	tables := map[string]table{}
	rows, err := pool.Query(ctx, `
		SELECT c.relname::text, c.relrowsecurity, c.relforcerowsecurity,
		       EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'org_id' AND a.attnum > 0 AND NOT a.attisdropped),
		       has_table_privilege('vellatry_tenant', c.oid, 'SELECT, INSERT, UPDATE, DELETE')
		FROM pg_class c
		WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind IN ('r', 'p')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		var tb table
		if err := rows.Scan(&name, &tb.rls, &tb.forced, &tb.orgID, &tb.tenantCanUse); err != nil {
			t.Fatal(err)
		}
		tables[name] = tb
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Permissive policies are ORed, so every one that reaches the tenant role counts:
	// a second one saying USING (true) would open the table whatever the first says.
	type policy struct{ name, using, check string }
	policies := map[string][]policy{}
	rows, err = pool.Query(ctx, `
		SELECT tablename::text, policyname::text, coalesce(qual, ''), coalesce(with_check, '')
		FROM pg_policies
		WHERE schemaname = current_schema() AND permissive = 'PERMISSIVE'
		  AND roles && ARRAY['vellatry_tenant', 'public']::name[]`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tableName string
		var p policy
		if err := rows.Scan(&tableName, &p.name, &p.using, &p.check); err != nil {
			t.Fatal(err)
		}
		policies[tableName] = append(policies[tableName], p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for name, tb := range tables {
		if reason, ok := sharedTables[name]; ok {
			if tb.tenantCanUse {
				t.Errorf("%s is shared (%s) but the tenant role can read or write it", name, reason)
			}
			continue
		}
		want, ok := ownRule[name]
		switch {
		case ok:
		case tb.orgID:
			want = orgRule
		default:
			t.Errorf("%s has no org_id column and is not in sharedTables: give it org_id and the tenant policy, or list it as shared with the reason", name)
			continue
		}
		if !tb.rls {
			t.Errorf("%s: row-level security is off, so every tenant reads every row", name)
		}
		if !tb.forced {
			t.Errorf("%s: row-level security is not forced, so the app's own login, which owns the table, bypasses it", name)
		}
		got := policies[name]
		if len(got) != 1 {
			t.Errorf("%s: %d policies reach the tenant role, want exactly one (%s)", name, len(got), want)
			continue
		}
		p := got[0]
		if using := oneLine(p.using); using != want {
			t.Errorf("%s: policy %s lets a tenant read %s, want %s", name, p.name, using, want)
		}
		// A policy for all commands with no WITH CHECK checks new rows with USING.
		if check := oneLine(p.check); check != "" && check != want {
			t.Errorf("%s: policy %s lets a tenant write %s, want %s", name, p.name, check, want)
		}
	}

	// The lists name real tables, which also stops the test passing on an empty scan.
	for name := range sharedTables {
		if _, ok := tables[name]; !ok {
			t.Errorf("%s is in sharedTables but there is no such table", name)
		}
	}
	for name := range ownRule {
		if _, ok := tables[name]; !ok {
			t.Errorf("%s is in ownRule but there is no such table", name)
		}
	}
}

// oneLine collapses the catalog's line breaks and indentation in a policy expression.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
