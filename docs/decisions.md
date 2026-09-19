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
| 28 | 2026-09-20 | Raw Search Console and GA4 facts: one BigQuery table per tenant and fact type, partitioned by day (partition expiry = retention), loaded by replacing whole day partitions. Postgres gets exact daily totals and bounded monthly top-N lists only. |
| 29 | 2026-09-20 | Google OAuth: the api only builds the consent URL and stores the sealed one-time code; the worker exchanges it, saves the sealed refresh token first, then lists properties. The api never calls Google. |
| 30 | 2026-09-20 | Credentials are sealed with AES-256-GCM bound to the org id (`internal/platform/secrets`); a ciphertext copied to another tenant's row does not open. |
| 31 | 2026-09-20 | A dead Google grant marks the connection broken (shown in the dashboard with a fix) and stops retrying; transient errors retry. Revoking wipes the token after revoking it at Google; synced history is kept. |
| 32 | 2026-09-20 | AI referral traffic is GA4 sessions whose source is an AI assistant (ChatGPT, Perplexity, Gemini, Copilot, Claude...), rolled up per day. |
| 33 | 2026-09-20 | Site crawls are ours, not a third party's: robots.txt is obeyed for `VellatryBot`, requests are paced, crawls are capped (300 pages) and weekly, plus on demand. A robots.txt that blocks us fails the crawl with that reason rather than being worked around. |
| 34 | 2026-09-20 | The crawler connects only to public addresses, checked on the resolved IP at connect time (covers redirects and DNS rebinding), because customers choose the domain and the crawler runs inside our network. |
| 35 | 2026-09-20 | Findings carry a stable fingerprint (rule + URL + key). A crawl resolves only findings on pages it actually visited; dismissed findings stay dismissed; a regression re-proposes its fix. |
| 36 | 2026-09-20 | Fixes are copy-ready and deterministic (robots.txt lines, llms.txt, Organization JSON-LD built from the brand setup). A fix goes live when the next crawl no longer finds the problem; the customer marks it sent, never live. |
| 37 | 2026-09-20 | Outbound messages: Slack incoming webhooks (validated to hooks.slack.com, sealed at rest) and email via Postmark, one copy per recipient, no open tracking. Email recipients must be team members or at the company's own domain. |
| 38 | 2026-09-20 | Watchers are code, not prompts. A repeat merges into the earlier notification (dedupe key per watcher and subject), immediate alerts are capped at 10 an hour per org (the rest wait for the digest), and each delivery is recorded per destination so a retried job never sends twice. |
| 39 | 2026-09-20 | Scheduled work (daily watchers, weekly digest) runs as hourly fan-outs that work out what is due, keyed by day or week, because River keeps its periodic schedule in memory and a restart would otherwise skip a Monday. |
| 40 | 2026-09-20 | Asana tasks are created only when someone asks (confirm first), found again by Asana's external id so a retry cannot duplicate them, closed with a comment when the fix is live or the blindspot resolves, and reopened if the problem comes back. |
| 41 | 2026-09-20 | Reports are frozen snapshots of stored rollups. Publishing copies the snapshot, summary and notes into an immutable version; a revision is a new version and old ones stay readable. |
| 42 | 2026-09-20 | One HTML template renders the hub's web view and the PDF (Gotenberg). The plain-text rendering of the same layout is the summary drafter's only input and the set of figures a summary may use. |
| 43 | 2026-09-20 | The model only suggests a summary (the `report_draft` purpose, one call per report); paragraphs with a figure the report does not show are dropped, and the suggestion is shown beside the editor, never published as is. |
| 44 | 2026-09-20 | CMO access: one hub per organisation at an unguessable URL, opened by an emailed one-time sign-in link (confirmed with a button, so mail scanners cannot use it up) for addresses at the brand's domain, extra domains the team adds (never public mail providers) or team members. Tokens are stored hashed; sessions last 30 days and end when a domain is removed; every view is logged. |
| 45 | 2026-09-20 | PDFs are stored in Postgres with the version (a few per organisation per month), so the api serves and logs them without calling object storage. |
