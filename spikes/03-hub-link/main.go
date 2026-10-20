// Mints a hub sign-in link for local work, where email is not configured and the link
// would otherwise never arrive. Reading the hub as a CMO does is the only way to see
// what they see, and that needs a session.
//
//	DATABASE_URL=postgres://vellatry_app:vellatry@localhost:5433/vellatry //	  go run ./spikes/03-hub-link cmo@example.com
//
// It grants nothing a DATABASE_URL does not already grant: anyone holding one can read
// every report directly. Never point it at production — not because it would break
// anything, but because a sign-in link that nobody asked for is not yours to make.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/reports"
)

func main() {
	ctx := context.Background()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	var org, slug string
	if err := db.InSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT org_id::text, slug FROM hubs ORDER BY created_at DESC LIMIT 1`).Scan(&org, &slug)
	}); err != nil {
		panic(err)
	}
	var token string
	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		token, err = reports.CreateLogin(ctx, tx, org, os.Args[1], time.Now())
		return err
	}); err != nil {
		panic(err)
	}
	fmt.Printf("http://localhost:8080/hub/%s/auth?token=%s\n", slug, token)
}
