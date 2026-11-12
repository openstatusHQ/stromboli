# Data access: on demand, MCP first

stromboli needs logs, metrics, deploys and machine state to investigate. How it gets them shapes its cost, its footprint and its safety.

## Query where the data lives

Many observability tools start by ingesting your data. stromboli doesn't: there is no ingestion endpoint, and it stores no logs or metrics ([ADR 0007](../adr/0007-query-logs-on-demand.md)). When an investigation runs, it queries the backend you already have (Loki, Axiom, CloudWatch, Cloud Logging, Prometheus, Fly's managed Prometheus) for the window it needs.

That choice has consequences in both directions.

In favour:

- **Small footprint.** A small VM and a Postgres database are enough. There is no pipeline to run, no storage to size, no retention to manage.
- **No second copy of sensitive data.** Logs stay where your access controls and retention already apply.
- **Nothing to set up per service** beyond a selector in the catalog.

Against:

- **Each investigation depends on the backend** being up and fast. A connector failure makes the brief poorer; it doesn't stop the investigation.
- **History is limited** to what the backend keeps. Baselines are computed on the fly from the preceding window rather than from long-term statistics.
- **Backend quirks show through.** Cloud Logging has no count tool, so counts stop at 5000; Fly has no log API at all, so its logs must be [shipped](../how-to/ship-fly-logs.md) to a store first.

## Aggregate before the model

Raw logs are a poor input for a model: large, repetitive and expensive. Connectors pre-aggregate:

- **Log patterns.** Lines are grouped into patterns, and the incident window is compared with the preceding window of the same length, so the model sees "this error is new, 340 vs 0" rather than 340 lines. JSON log lines are reduced to their message and level so patterns are not split by host or region.
- **Metric summaries.** Each series comes back as min, max, avg, last, its baseline average and 12 points, capped at 10 series.
- **Hard limits** on windows, rows and bytes, then [redaction](safety-model.md#protecting-data).

The model can still read raw lines with `query_logs` when a pattern needs a closer look, but it starts from the summary.

What is stored is the evidence each investigation actually used: the tool inputs and their aggregated, redacted output in the audit trail, so a report stays verifiable after the backend's retention expires.

## MCP first, else the provider's API

There are two ways to reach a provider: the provider's own MCP server, or its API. stromboli prefers the MCP server **the provider hosts**, when there is one, and otherwise calls the API directly ([ADR 0011](../adr/0011-mcp-first-connectors.md)).

| Provider | Through | Why |
|---|---|---|
| Google Cloud Logging and Monitoring | Google's remote MCP servers | hosted by Google, tools annotated read-only |
| GitHub (with a token) | GitHub's remote MCP server, read-only mode | hosted by GitHub; deployments have no MCP tool, so they use REST |
| Fly.io | Machines API and managed Prometheus | Fly's MCP is `flyctl mcp server`, a local process that serves every flyctl command (destroy, exec, secrets) and marks none read-only |
| CloudWatch | AWS SDK | AWS's MCP server is a local Python process |
| Loki, Prometheus | their HTTP APIs | no hosted MCP server; Grafana's needs a Grafana instance in front |
| Axiom | its remote MCP server, logging in with OAuth | the OAuth login has the user's access. `queryDataset` is not marked read-only (it can save a result snapshot, which stromboli never asks for), so it is allowed by name |
| Vercel | its remote MCP server, logging in with OAuth | hosted by Vercel, tools annotated read-only; it accepts no access token |

Two principles sit behind the table:

- **Never add a process just to get MCP.** A local MCP server is one more thing to install, update and secure, and often wraps a CLI with far more power than stromboli needs.
- **stromboli is the client, not the model.** stromboli calls MCP tools from code and turns their output into compact evidence. The model never sees these servers or their tool lists.

On top of that, each connector names the few tools it may call, and any other tool is refused, even if the server offers `deploy`, `delete` or `exec`. For servers that annotate their tools, connecting fails unless every allowed tool is marked `readOnlyHint`. Sessions are reused per connector and closed on shutdown.

## Metrics through one API

Metrics go through the Prometheus HTTP API everywhere it exists ([ADR 0012](../adr/0012-metrics-through-prometheus-api.md)): Prometheus itself, Mimir, Thanos, VictoriaMetrics, Grafana Cloud, Fly's managed Prometheus, and PromQL on Google Cloud Monitoring. One query language means one `get_metrics` tool, presets written once per platform, and a model that already knows PromQL.
