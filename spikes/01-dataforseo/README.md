# Spike 1: DataForSEO cost and quality

Answers the question that sets pricing: what does one AI answer cost, how long does it
take, how often is it grounded in sources, and does deterministic brand detection hold up
on real Australian answers?

Runs every query in `config.example.json` (two sets, 12 queries, Australian brands) on
ChatGPT, Gemini and Google AI Overview, writes one JSON line per answer to
`results/<run>/results.jsonl`, and a summary with a monthly cost projection to
`results/<run>/summary.md`. No LLM is called anywhere.

## Safety

- `-budget-usd` (default 3) is a hard cap. Every paid call reserves budget first and the
  run stops before it can exceed the cap. Answers already paid for are always recorded.
- Results are written as they arrive, so Ctrl-C keeps everything paid for.
- `-dry-run` uses a fake API: no credentials, no cost. Use it first.

## Run

```bash
cp ../../.env.example ../../.env    # then fill in DATAFORSEO_LOGIN / DATAFORSEO_PASSWORD
go run . -dry-run                   # check it works, costs nothing
```

Then the real runs, in this order (PowerShell: set the two variables with `$env:NAME = "..."`):

```bash
# 1. Live fast lane, both ChatGPT web-search settings: 96 answers
go run . -web-search both

# 2. Standard queue, same queries: compares price and time-to-answer
go run . -mode standard

# 3. Confirmation depth on one set, for real mention bands
go run . -attempts 5 -engines chatgpt,gemini -config config.example.json
```

Useful flags: `-engines chatgpt,gemini,ai_overview`, `-attempts N`, `-concurrency 4`,
`-async-aio` (ask for asynchronously loaded AI Overviews; confirm the parameter in
DataForSEO's docs first), and the projection scenario `-proj-topics 40 -proj-prompts 3
-proj-confirm 0.3 -proj-tracked 100`.

## What to decide from the results

| Result | Decision it feeds |
| --- | --- |
| Cost per answer, live vs standard | Plan tiers: daily answer budget per plan, and whether background work always uses the standard queue |
| Monthly projection per tenant | Minimum viable price per plan (answer cost is the largest variable cost) |
| Latency p50/p95 and standard-queue wait | Whether "Check now" can promise seconds, and how fresh background results can be |
| ChatGPT with web search off vs on: sources, fan-out, mention rate | Keep `force_web_search` off (matches real users) or not |
| AI Overview present rate for Australian queries | How much of Blindspots relies on AI Overview |
| Variation between attempts of the same query | Whether 2-attempt screening is informative before 5-attempt confirmation |
| Detection cross-check disagreements | Aliases and exclusions to add; the first hand-labelled examples for the gold set |
| Response shapes (`brand_entities`, `fan_out`, `sources`) | Fix the parser in `internal/dataforseo` if the live shape differs from the fixtures |

The fixtures in `internal/dataforseo/testdata` were written from DataForSEO's documented
response shapes. The first live run is the real check: if a field comes back empty, look
at the raw response and adjust `parseLLM` / `parseSERP`.

## Account

Create your own DataForSEO account (do not reuse an employer's credentials). It is
pay-as-you-go with a minimum top-up; check the current amount on their pricing page.
