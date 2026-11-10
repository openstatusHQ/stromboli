# Architecture

stromboli is a single Go binary and a Postgres database. The binary is both the server and its CLI. One instance serves one organisation: there is no multi-tenancy, and isolation comes from running separate instances ([ADR 0002](../adr/0002-self-hosted-one-instance-per-organisation.md)).

```text
                 alert sources                 people and agents
   openstatus · Alertmanager · Grafana ·    CLI · gRPC/Connect clients ·
       generic · health checks                MCP clients · Slack
                     │                                  │
                     ▼                                  ▼
            ┌────────────────────────────────────────────────────┐
            │                stromboli serve                     │
            │                                                    │
            │  webhooks ──▶ trigger ──┐     ConnectRPC API       │
            │                         ▼     (catalog, connectors,│
            │  health checks ──▶  agent engine   investigations, │
            │                     │   │   │      actions)        │
            │            brief ◀──┘   │   └──▶ action service ───┼──▶ Fly Machines API
            │              │      LLM tool loop      ▲           │    (after approval)
            │              ▼          │              │           │
            │         connector registry ◀───────────┘           │
            │              │          │                          │
            │              │          └──▶ notifier (Slack) ─────┼──▶ Slack
            └──────────────┼─────────────────────────────────────┘
                           │                    │
          logs · metrics · deploys · machines   ▼
          (queried on demand, read-only)     Postgres: catalog, connectors,
                                             investigations, steps, actions
```

## The pieces

**API first.** Everything the CLI does goes through a public [ConnectRPC API](../reference/api.md): `CatalogService`, `ConnectorService`, `InvestigationService` and `ActionService`, defined in protobuf. The same endpoints speak Connect, gRPC, gRPC-Web and plain JSON, so a future UI, scripts and other tools use the same contract as the CLI ([ADR 0005](../adr/0005-go-binary-api-first-connectrpc.md)). The [MCP server](../reference/mcp-tools.md) is a thin layer over the same services.

**Triggers.** Webhooks normalise each alert source into one shape (title, services, fingerprint, time, resolved), match it to catalog services, and hand it to the engine. Health checks are another trigger: they read metrics on a timer and start an investigation when a threshold is crossed.

**The agent engine** owns investigations. It builds a deterministic brief, then runs one LLM conversation over read-only tools until a mitigation and a report are submitted. Investigations run in the background, outlive the request that started them, and are bounded by a timeout and a tool budget.

**The connector registry** turns a connector stored in Postgres (with encrypted secrets) into a client for Loki, Axiom, CloudWatch, Google Cloud, Prometheus, Fly.io or GitHub. It reuses MCP sessions per connector and closes them on shutdown.

**The action service** stores machine actions proposed by investigations, and runs one only after a human approves it.

**The notifier** posts to Slack: one message per investigation, edited in place, with thread replies for what should notify on-call. Without Slack, everything is still available through the API.

**Postgres** holds all state: the catalog and runbooks, connectors, investigations with their audit trail, and actions ([ADR 0004](../adr/0004-postgres-only-storage.md)). Logs and metrics are not stored. Migrations run on startup.

## Where it lives in the code

| Package | Job |
|---|---|
| [`cmd/stromboli`](../../cmd/stromboli) | the CLI and `serve` |
| [`internal/server`](../../internal/server) | wires everything into one HTTP server |
| [`internal/config`](../../internal/config) | reads the environment |
| [`internal/api`](../../internal/api) | ConnectRPC handlers and auth |
| [`internal/trigger`](../../internal/trigger) | alert webhooks |
| [`internal/agent`](../../internal/agent) | the brief, the tool loop, the tools and the system prompt |
| [`internal/llm`](../../internal/llm) | provider adapters: Anthropic Messages, OpenAI-compatible, Vercel AI Gateway |
| [`internal/catalog`](../../internal/catalog) | catalog YAML, validation, dependency graph, Fly helpers |
| [`internal/connector`](../../internal/connector) | the registry and one package per data source (`logs`, `metrics`, `deploys`, `fly`, `gcp`, `mcpconn`) |
| [`internal/action`](../../internal/action) | machine actions: propose, approve, run |
| [`internal/healthcheck`](../../internal/healthcheck) | scheduled metric checks |
| [`internal/report`](../../internal/report) | Markdown rendering, Slack messages and buttons |
| [`internal/mcpserver`](../../internal/mcpserver) | the MCP tools, over stdio and HTTP |
| [`internal/policy`](../../internal/policy) | redaction before data reaches the model |
| [`internal/secrets`](../../internal/secrets) | AES-256-GCM encryption of connector secrets |
| [`internal/store`](../../internal/store) | Postgres access, migrations and generated queries |
| [`proto/stromboli/v1`](../../proto/stromboli/v1), [`gen`](../../gen) | the API definition and generated code |

## Why one instance

A single process keeps operations simple: one container, one database, no queue. It also shapes some behaviour: alert dedupe relies on one database view of running investigations, and health checks run in every process, so a second replica would investigate the same breach twice. The deploy guides keep it to one machine for that reason. Investigations that were running when the process stops are marked failed on the next start, as are machine actions, which are worth checking by hand.

## Why a CLI in the same binary

The binary that runs the server is also the client, so there is nothing else to install, and versions always match. The CLI follows [clig.dev](https://clig.dev) ([ADR 0015](../adr/0015-cli-follows-clig.md)): data on stdout, messages on stderr, `--json` for scripts, confirmation before destructive commands, and secrets never required on the command line.
