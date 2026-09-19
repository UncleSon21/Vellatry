# Vellatry

Find your AI blindspots, fix them, prove it to your CMO.

Vellatry shows Australian in-house marketing teams where ChatGPT, Gemini and Google
AI Overview leave their brand out, next to their Google Search data, and turns each
blindspot into a fix whose outcome is measured.

- Design doc: https://claude.ai/code/artifact/cfdaeb64-419b-4cc9-b5a7-4bb1751a6876
- Decisions (short form): [docs/decisions.md](docs/decisions.md)
- Rules for contributors and Claude: [CLAUDE.md](CLAUDE.md)

## Status

- **M0 foundation:** done. Row-level security, tenant isolation tests, River job queue,
  event bus, LLM gateway with purpose/size/budget guards, architecture tests, CI.
- **M1 Visibility engine:** backend done. Brand setup, topics and prompts,
  budget-planned discovery (screen with 2 answers, confirm with 5), DataForSEO
  collection, deterministic detection and gap matrix, blindspots, the judge, rollups,
  and the API for Performance, Blindspots, Sources and the live event stream.
  Needs DataForSEO credentials to request real answers.
- **M2 Search Console + GA4:** backend done. Google OAuth (exchange in the worker),
  sealed tokens, week-sized backfill jobs, BigQuery day-partition loads, Postgres
  rollups (daily totals, monthly top queries/pages/landing pages, channels, AI
  referrals), reconciliation, broken-connection handling, and the API.
- **M3 Site and Fixes:** backend done. Weekly and on-demand crawls (robots.txt obeyed,
  sitemaps and sitemap indexes, paced, capped at 300 pages, public addresses only),
  AI-bot access and llms.txt checks, on-page rules with stable fingerprints, findings
  that open and resolve themselves, copy-ready fixes (robots.txt lines, llms.txt,
  Organization JSON-LD) that move proposed -> sent -> live when the next crawl confirms
  them, and the API.
- **M4a Automations:** backend done. Slack (incoming webhooks) and email (Postmark)
  destinations, each tested on creation and marked broken when they stop working;
  watchers (visibility drop, competitor overtakes, critical site issue, broken
  connection, confirmed blindspot) evaluated by code with merged repeats and an hourly
  cap; the weekly digest (no model, sections without data left out); Asana tasks for
  fixes and blindspots, created on request and closed when Vellatry sees them resolved.
- **M4b Reports hub:** backend done. Report series (calendar month, Australian FY
  quarter, financial year, custom) drafted automatically once the period's data has
  settled; frozen snapshots; sections with no data left out of the report (the team
  sees why); an optional model-suggested summary that keeps only paragraphs whose
  figures appear in the report; a figure check on the team's own words before
  publishing; immutable versions; one HTML template for the web view and the PDF
  (Gotenberg); a private hub for the CMO with emailed one-time sign-in links limited
  to the company's domains, every view logged; recipients get a link, never an
  attachment.
- Next: M5 keyword research, M6 agent, the dashboard UI.

| Spike | Question | State |
| --- | --- | --- |
| [01-dataforseo](spikes/01-dataforseo) | Cost, latency and quality of AI answers for Australian queries | Harness built, needs a DataForSEO account |
| 02-bigquery | Search Console ingestion at large-site scale | Not started |
| 03-jobs | River for fan-out, postback signals, approval waits | Not started |
| 04-agent | Router + analyses + validator | Not started |

## Layout

```
cmd/vellatry/   one binary: migrate | api | worker
internal/       production packages (platform/, api/, visibility/, site/, dataforseo/, google/, archtest/)
spikes/         throwaway experiments
deploy/         local Postgres
docs/           decisions and notes
```

## Run locally

Requires Go 1.26+ and Docker.

```bash
docker compose -f deploy/docker-compose.yml up -d
export DATABASE_URL=postgres://vellatry_app:vellatry@localhost:5433/vellatry
go run ./cmd/vellatry migrate
go run ./cmd/vellatry api      # http://localhost:8080/healthz
go run ./cmd/vellatry worker
```

## Run the tests

```bash
export VELLATRY_TEST_DATABASE_URL=postgres://vellatry_app:vellatry@localhost:5433/vellatry_test
go test ./...
```

Without `VELLATRY_TEST_DATABASE_URL` the database tests are skipped.
