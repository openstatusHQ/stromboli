# 0007. Query data on demand, store only the evidence

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

Teams already pay for a log store and a metrics backend. Ingesting their data would make opensre a second, expensive observability system. Raw log firehoses are also useless to an LLM: they waste context and bury the signal.

## Decision

- opensre does **not ingest or store logs or metrics.** It queries the customer's backend when an investigation runs. There is no ingestion endpoint.
- **Connectors pre-aggregate** before anything reaches the model: top error patterns with counts, the incident window compared with the preceding window of the same length, and a handful of sample lines. Metric series come back as min, max, avg, last, baseline average and 12 points, capped at 10 series.
- Hard limits apply to time windows, rows and bytes, and output is redacted.
- **What is stored** is the evidence each investigation used: the exact query, the window, the aggregated result and at most about 20 redacted sample lines. Reports stay verifiable after the backend's retention expires, and the evidence can be replayed for evaluation.
- Later, optionally: small per-service baselines (top error fingerprints and hourly counts) so the agent can say "this pattern is new this week".

## Consequences

- opensre adds no storage cost proportional to traffic.
- Investigations depend on the backend being reachable during the incident.
- Aggregation quality matters more than prompt tweaks, so it is where connector work goes.
