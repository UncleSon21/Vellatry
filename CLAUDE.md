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

## Stack

- Go (one module at the repo root). Next.js dashboard will live in `web/`.
- Postgres + pgvector for app data, BigQuery for raw Search Console / GA4 facts.
- River (Postgres-backed) for jobs. Add a workflow engine only if a spike proves the need.
- Python only for model training and the inference service (`ml/`, later).

## Layout

- `cmd/` binaries (`vellatry api|worker|migrate`, later)
- `internal/` production packages
- `spikes/NN-name/` throwaway experiments; may import `internal/`, never imported by it
- `docs/` decisions and notes

## Commands

Go is not installed on the dev machine yet; either install Go 1.25+ or run via Docker:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.25 go test ./...
```

Tests use the standard `testing` package only.
