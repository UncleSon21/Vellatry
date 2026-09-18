# Vellatry

Find your AI blindspots, fix them, prove it to your CMO.

Vellatry shows Australian in-house marketing teams where ChatGPT, Gemini and Google
AI Overview leave their brand out, next to their Google Search data, and turns each
blindspot into a fix whose outcome is measured.

- Design doc: https://claude.ai/code/artifact/cfdaeb64-419b-4cc9-b5a7-4bb1751a6876
- Decisions (short form): [docs/decisions.md](docs/decisions.md)
- Rules for contributors and Claude: [CLAUDE.md](CLAUDE.md)

## Status

Pre-M0. Running spikes to answer the risky questions before building.

| Spike | Question | State |
| --- | --- | --- |
| [01-dataforseo](spikes/01-dataforseo) | Cost, latency and quality of AI answers for Australian queries | Harness built, needs a DataForSEO account |
| 02-bigquery | Search Console ingestion at large-site scale | Not started |
| 03-jobs | River for fan-out, postback signals, approval waits | Not started |
| 04-agent | Router + analyses + validator | Not started |

## Layout

```
cmd/        binaries (later)
internal/   production packages
spikes/     throwaway experiments
docs/       decisions and notes
```

## Run the tests

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.25 go test ./...
```
