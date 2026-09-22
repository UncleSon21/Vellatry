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
- **M5 Topics and keyword research:** backend done, and it makes no LLM call at all.
  Seeds (Search Console positions 11-30, the team's topics, anything they paste) are
  expanded with DataForSEO Labs, the competitors' ranking keywords fill the gap, and
  each keyword's top ten results are fetched on the standard queue. Keywords that share
  four of those results become one topic, named after its highest-volume keyword, with
  intent from the results themselves. Each topic is mapped to the page Google already
  ranks (else where the brand appears, else the closest page on the site), scored for
  opportunity with every component visible, and flagged when it has no page, the page
  answers a different need, or two of the brand's own pages compete. New topics are
  proposed; approving one is what starts Vellatry measuring it.
- **M6 Agent:** backend done. A router recognises the questions teams actually ask and
  answers them in the api from stored rows, with no external call and nothing to wait
  for: eleven analyses, each computing its own numbers and choosing its own headline.
  Anything it does not recognise goes to the worker, where a model may only choose an
  analysis from the catalogue and put its numbers into sentences; any figure that is not
  in the evidence is dropped and the code's own wording is shown instead. Actions are
  proposals until someone confirms them.
- **Onboarding:** a six-step setup wizard (brand, what you sell, why you, competitors,
  connections, blindspots setup) that resumes where the team left off. The first step
  creates the organisation and reads the site straight away; its own structured data
  and navigation pre-fill other names for the brand and propose starter topics, which
  never overwrite anything a person entered. "Test my setup" highlights what detection
  would count in a real AI answer or pasted text, using the same matcher detection
  uses, on edits that are not saved yet. Finishing (at least one tracked topic) starts
  keyword research. Settings → Brand edits the same setup afterwards.
- **Landing page and sign-in:** `/` introduces the product: every claim describes
  shipped behaviour, and the illustrations use no real brands or customer figures.
  Sign-in and sign-up are Clerk, themed from the app's own tokens. The session token
  is fetched per request and never stored. Without a Clerk key (local development),
  Clerk is not loaded and the api's header sign-in is used. The app starts at `/today`.
- **Design:** "paper and highlighter": Archivo throughout, warm paper, lime for your
  mentions and coral for a competitor's. The landing page shows the product itself in a
  self-playing tour (Today, a blindspot, Test my setup, Fixes) and plays a live check in
  the hero. `.claude/skills/vellatry-design` holds the tokens, type, motion rules,
  components, copy rules and the checks before a design is done, so every session
  designs the same way.
- **Dashboard (`web/`):** Next.js, no UI framework and no chart library. Overview,
  Performance, Blindspots, Sources, Search, Topics, Site, Fixes, Reports, Automations
  Brand and Connections, each reading the api and nothing else, following the event stream
  instead of polling. The agent is a panel on every page and sends the page and its
  date range with the question.
- Next: M7 (notebook and the CMO report bot), and the first design partner.

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
web/            the dashboard (Next.js); talks to the api and nothing else
spikes/         throwaway experiments
deploy/         local Postgres, the managed-Postgres bootstrap, Gotenberg on Fly
docs/           decisions, notes, and how to deploy
Dockerfile      the api and worker image; fly.toml runs it on Fly.io
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

## Deploy

The api and worker run on Fly.io in Sydney from one image, Postgres on Neon in Sydney,
and the dashboard on Vercel. Step by step, including the checks that refuse an unsafe
production setup: [docs/deploy.md](docs/deploy.md).
