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
