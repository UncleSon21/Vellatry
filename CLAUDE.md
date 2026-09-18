# Vellatry — rules for Claude

AI-visibility and SEO intelligence SaaS for Australian in-house marketing teams.
Full design: https://claude.ai/code/artifact/cfdaeb64-419b-4cc9-b5a7-4bb1751a6876
Key decisions in short form: `docs/decisions.md`.

## Non-negotiables (the two failures of the system this replaces)

1. **No LLM spend on per-item paths.** Nothing that runs per answer, per keyword,
   per page or per query may call an LLM. Use code, or a model we trained.
   - Only `internal/gateway` may call an LLM provider. Nowhere else imports an LLM
     HTTP client or hard-codes a provider URL.
   - Every gateway call names a `purpose`. Per-item purposes are refused unless
     explicitly allow-listed (only the batch-API judge during the design-partner phase).
   - Every purpose has a maximum input size. Build prompts from explicit fields,
     never from a whole row or `SELECT *`.
   - Every paid call (LLM or DataForSEO) checks a budget first and fails closed.
     Identical inputs are cached by hash and never paid for twice.
2. **Nothing the user waits on waits for an external call.**
   - The `api` role reads stored results only. It never imports provider clients
     (DataForSEO, Google, LLM gateway). External work is enqueued, never awaited.
   - Work is split per answer / per item. No end-of-run barriers, no fan-in
     counters, no sweeps. Fan out only where work is genuinely parallel.
   - Every external call has a per-call timeout, retries with backoff on transient
     errors only, and goes through a per-provider rate limiter.

## Other rules

- **Deterministic first.** Scores, counts, positions, bands and gaps are computed by
  code. A model never produces a number that is shown to a user.
- **Tenancy.** Every tenant table has `org_id` and row-level security with `FORCE`.
  Tenant work runs inside a transaction that sets the org with `SET LOCAL`.
- **Idempotent jobs.** Any job may run twice. Use upserts or partition replacement.
- **Versioned methods.** Every stored score records the method/model version.
- **Errors.** Transient (429, 5xx, timeouts, connection resets) retry. Permanent
  (billing, auth, 40x, parse failures) fail visibly; billing/auth trips a global halt.
- **Absence rule.** Dashboard shows missing or broken connections with a fix action.
  The exported CMO report omits missing sections. No data means no report.
- **Secrets** come from environment variables. Never commit `.env`.
- Do not copy code, prompts or data from any previous employer's system.

## How the rules are enforced

- `internal/archtest` fails the build if: an LLM host appears outside the gateway, an LLM
  SDK is imported anywhere, DataForSEO's host appears outside its client, `internal/api`
  depends (even transitively) on an external client, or any SQL uses `SELECT *`.
- `internal/platform/gateway`: every LLM call names a purpose from `DefaultPurposes()`
  (the full inventory of LLM use). Unknown or per-item purposes, oversize prompts and
  over-budget calls are refused before sending; identical requests hit the cache.
- `internal/platform/budget`: fail-closed spend caps shared by every paid client.

## Database rules

- Tenant work: `db.InTenant(ctx, pool, orgID, fn)`. Cross-tenant work (scheduler, signup,
  admin): `db.InSystem`. A query that uses neither sees no rows: RLS is forced and the
  connecting owner matches no policy.
- Never connect as a superuser; superusers bypass row-level security. Local and test
  databases use `vellatry_app` (see `deploy/`).
- Untrusted SQL (the future agent query tool) gets its own low-privilege login role and
  connection, never the app pool: the owner can always `SET ROLE`.
- Events: `events.Bus.Emit` inside the caller's transaction writes the event and one job
  per subscriber atomically. The job queue is the outbox; there is no poller.
- `db.AsOwner` exists only to write queue rows inside a tenant transaction. Never use it
  for tenant tables.
- Migrations: `internal/platform/db/migrations/NNNNN_name.sql` (goose), next free number.

## Stack

- Go 1.26+ (one module at the repo root). Next.js dashboard will live in `web/`.
- Postgres + pgvector for app data, BigQuery for raw Search Console / GA4 facts.
- River (Postgres-backed) for jobs. Add a workflow engine only if a spike proves the need.
- Python only for model training and the inference service (`ml/`, later).

## Layout

- `cmd/vellatry` one binary, roles `migrate | api | worker`
- `internal/api` the api role (reads stored results, enqueues work)
- `internal/platform/` db, jobs, events, gateway, budget, metering
- `internal/` domain packages (`visibility/...`, `dataforseo`, ...)
- `internal/archtest` architecture rules as tests
- `spikes/NN-name/` throwaway experiments; may import `internal/`, never imported by it
- `deploy/` local Postgres (docker compose)
- `docs/` decisions and notes

## Commands

```bash
docker compose -f deploy/docker-compose.yml up -d
go test ./...        # database tests run only when VELLATRY_TEST_DATABASE_URL is set
```

Test database: `postgres://vellatry_app:vellatry@localhost:5433/vellatry_test`.
Tests use the standard `testing` package only.
