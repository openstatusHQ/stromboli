# 0012. Metrics through the Prometheus HTTP API

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

The brief and the agent need per-instance metrics (memory, CPU, throttling, OOM kills, request rate, 5xx, latency). Most of our users' metrics backends speak PromQL, and Fly.io runs a managed Prometheus for every organisation.

## Decision

- One Prometheus HTTP API client serves two connector kinds: `prometheus` (Prometheus, Mimir, Thanos, VictoriaMetrics, Grafana Cloud) and `fly` (Fly.io's managed Prometheus, with presets for machine and proxy metrics). Google Cloud Monitoring is queried with PromQL through Google's MCP server ([0011](0011-mcp-first-connectors.md)).
- A service's `metrics.selectors` are PromQL label matchers, substituted for `$selector` in queries.
- Where we know the metric names (Fly, Pub/Sub, Cloud Tasks, Cloud Run), connectors offer named presets. Elsewhere the agent uses `list_metrics` and writes PromQL.

## Consequences

- One client covers most backends.
- Without presets, results depend on the model writing correct PromQL.
