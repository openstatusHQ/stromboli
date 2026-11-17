# 0023. Metrics through a self-hosted prometheus-mcp, opt-in

- **Status:** Proposed
- **Date:** 2026-11-15

## Context

`prometheus` and `fly` connectors call the Prometheus HTTP API directly ([0012](0012-metrics-through-prometheus-api.md)), so stromboli holds the backend's URL and credentials, including a Fly org token.

Some teams already run [prometheus-mcp](https://github.com/freepik-company/prometheus-mcp), an open-source MCP server for Prometheus-compatible backends (Prometheus, Mimir, Thanos, Fly's managed Prometheus), and would rather keep metrics credentials there, behind one audited gateway that other MCP clients use too.

[0011](0011-mcp-first-connectors.md) says stromboli goes through MCP only when the provider hosts the server, and never adds a process just to get MCP. prometheus-mcp is neither hosted by the provider nor something stromboli starts: the user runs it.

## Decision

- `prometheus` and `fly` connectors take an optional `mcp_url`. With it, metrics go through that prometheus-mcp server over Streamable HTTP; without it, nothing changes. This amends 0011: a self-hosted MCP server is allowed when the user already runs it and opts in, never as a requirement and never started by stromboli.
- The backend's URL and credentials live in prometheus-mcp's config. With `mcp_url`, the connector rejects `url` and the HTTP credentials (`username`, `password`, `bearer_token`). `mcp_token` is sent as a bearer token to the server; `backend` picks one of its backends; `tenant_id` is passed as `org_id`.
- Only `prometheus_query` and `prometheus_range_query` are allowed. prometheus-mcp does not annotate its tools, so both are listed as `ReadOnlyUse`: they only evaluate PromQL.
- Results are read from `structuredContent`, which mirrors the Prometheus API's `data` object since prometheus-mcp v0.8.0. Older servers answer only in TOON text and are reported as too old.
- prometheus-mcp exposes no label values API to stromboli, so metric names and service discovery use `group by (<label>) ({<selector>})` range queries sampled at least every 5 minutes (discovery: every 30 minutes over 24 hours).
- A `fly` connector keeps `org`, and `token` becomes optional with `mcp_url`: it is only used to list machines through the Machines API.

## Consequences

- Teams can keep Prometheus and Fly credentials out of stromboli, and share one metrics gateway with their other MCP clients.
- One more service to run and secure for those who opt in. prometheus-mcp only offers JWT validation for its clients, so it belongs on a private network.
- Discovery and `list_metrics` sample the window instead of reading the label index: a series that only reported between samples is missed, and the queries cost more than label values calls on a large backend.
- The connector depends on a third-party server's tool names and result shape; tests pin them, and connecting fails if the tools disappear.
