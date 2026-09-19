# Decisions

Short form of the design doc. One line per decision, newest last. Change a decision by
adding a new line that supersedes the old one, not by editing history.

| # | Date | Decision |
| --- | --- | --- |
| 1 | 2026-09-18 | Target Australian in-house marketing teams first; agencies later. One account = one company = one brand + competitors. |
| 2 | 2026-09-18 | Go modular monolith (`api`, `worker` roles) + Next.js dashboard. Python only for ML training and inference. |
| 3 | 2026-09-18 | Postgres (+pgvector) for app data; BigQuery for raw Search Console / GA4 facts, nightly partition replacement, rollups to Postgres. No NoSQL. |
| 4 | 2026-09-18 | Hosting on Google Cloud, Sydney region. Clerk for auth, with our own user/org/membership tables. |
| 5 | 2026-09-18 | Every tenant table carries `org_id` with row-level security (`FORCE`). |
| 6 | 2026-09-18 | Three ways to run work: interactive reads, River jobs, outbox events. Workflow engine only if spike 3 proves the need. |
| 7 | 2026-09-18 | AI engines v1: ChatGPT and Gemini (DataForSEO LLM Scraper), Google AI Overview (DataForSEO SERP). Location 2036, English. |
| 8 | 2026-09-18 | Visibility engine runs continuously on a daily budget: Discover (Blindspots, screen with 2 attempts, confirm with 5) and Track (Performance). Topic-first; no batch audit, no end-of-run clustering. |
| 9 | 2026-09-18 | Gap matrix + absolute mention-rate bands (0-1 blindspot, 2-3 weak, 4-5 visible, minimum 3 successful attempts). New differentiator gap. |
| 10 | 2026-09-18 | Rubric keeps four families (Visibility, Sentiment, Confidence, Completeness) on a 1-10 display; facts computed by code; judged dimensions labelled on 5-point anchored scales with evidence spans. |
| 11 | 2026-09-18 | No LLM on per-item paths. LLM judge via batch API only during the design-partner phase; trained models replace it before general launch. |
| 12 | 2026-09-18 | Keyword research makes zero LLM calls: search-result overlap clustering, Search Console page mapping, trained relevance and page-type classifiers. |
| 13 | 2026-09-18 | Agent is a pipeline: router -> deterministic analyses -> code review -> narration -> claim validation -> confirm-first actions. LLM is a fallback. |
| 14 | 2026-09-18 | Fixes: instructions + Asana in v1; CMS drafts phase 2; GitHub PRs later and premium. No JavaScript-injected fixes. |
| 15 | 2026-09-18 | Slack, email and Asana are outbound only. No Slack bot. |
| 16 | 2026-09-18 | ChatGPT `force_web_search` off by default to match real user behaviour; spike 1 measures both settings. |
| 17 | 2026-09-18 | Row-level security uses two roles chosen per transaction (`vellatry_tenant`, `vellatry_system`) with policies scoped `TO` each role, not a settable flag; the connecting owner matches no policy, so an unscoped query sees nothing. |
| 18 | 2026-09-18 | River is the job queue and the outbox: events and subscriber jobs are written in the same transaction; no poller. |
| 19 | 2026-09-18 | Architecture rules are tests (`internal/archtest`), not conventions. |
| 20 | 2026-09-18 | The app never connects as a Postgres superuser (superusers bypass row-level security). |
| 21 | 2026-09-20 | The Anthropic provider uses the official Go SDK (per Anthropic's guidance), confined to `internal/platform/gateway`; archtest now allows LLM SDKs there and nowhere else. |
| 22 | 2026-09-20 | Cells are recomputed from the answers stored for their round, never incremented, so every collection job is idempotent. Rollups increment only when an answer is newly inserted. |
| 23 | 2026-09-20 | Answer collection snoozes (River `JobSnooze`) until DataForSEO's task is ready: no poller, no pingback required. |
| 24 | 2026-09-20 | The per-item judge is off unless `VELLATRY_ALLOW_JUDGE=1`, runs only on answers that mention the brand, keeps only evidence quoted verbatim from the answer, and never judges a truncated answer. |
| 25 | 2026-09-20 | Job arguments (`internal/jobargs`) and event subscriptions (`internal/domainevents`) live in dependency-free packages so the api role can enqueue work without importing external clients. |
| 26 | 2026-09-20 | Live dashboard updates: an `events` insert trigger `pg_notify`s ids only; one LISTEN connection per api process fans them out over SSE, filtered by org. |
| 27 | 2026-09-20 | Dismissed blindspots stay dismissed through later answers: the customer's decision wins. |
