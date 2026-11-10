# 0021. stromboli as an MCP server

- **Status:** Proposed
- **Date:** 2026-11-09

## Context

Engineers already work in MCP clients: Claude Code, Claude Desktop, Cursor, coding agents. During an incident they want to ask "what is stromboli seeing on `api`?", read the report, or start an investigation without leaving that client. Today stromboli only speaks MCP as a client ([0011](0011-mcp-first-connectors.md)).

Constraints:

- stromboli suggests and never acts on its own ([0006](0006-suggest-dont-execute.md)). Whatever drives an MCP client is a model, so the MCP surface must not widen what a model can do.
- Approved machine actions need a named human ([0013](0013-approved-fly-machine-actions.md)). A model approving its own proposal would defeat that.
- One binary, one port, API first ([0005](0005-go-binary-api-first-connectrpc.md)). The Go MCP SDK (`github.com/modelcontextprotocol/go-sdk`) is already a dependency.

## Decision

### Transport

- **Streamable HTTP at `POST /mcp`**, on the same mux and port as the Connect API, served with `mcp.NewStreamableHTTPHandler`. Sessions are kept in memory only for the client's name (for the audit trail) and dropped after 30 minutes idle; a restart makes clients reconnect, which they do on their own.
- **`stromboli mcp`**, a CLI subcommand serving the same tools over **stdio**, for clients that only launch local processes. It is a client of the public API (`STROMBOLI_URL`, `STROMBOLI_API_TOKEN`), like every other CLI command.
- Off by default: `STROMBOLI_MCP_ENABLED=true` turns on `/mcp`, and requires `STROMBOLI_API_TOKEN` (as `STROMBOLI_ACTIONS_ENABLED` does).

### One implementation, on top of the API

- A new package `internal/mcpserver` registers the tools. It depends only on the Connect service interfaces (`stromboliv1connect.*ServiceHandler` unary methods), never on the store or the engine.
- In `stromboli serve` it is given the in-process `api.*Service` values; in `stromboli mcp` it is given Connect clients. Connect's generated unary client and handler methods have the same signatures, so both modes share the tool code, and the MCP surface can never do something the API cannot. `wait_investigation` polls `GetInvestigation` every second, as `WatchInvestigation` polls the database, so the one streaming RPC (whose client and handler shapes differ) is not needed.

### Auth

- `/mcp` checks `Authorization: Bearer <token>` with the same constant-time comparison as `api.NewAuthInterceptor`, as plain HTTP middleware.
- An optional `STROMBOLI_MCP_TOKEN` replaces the API token on `/mcp`. Its holder only reaches the tools below, never the full API, so it is the token to hand to an MCP client.
- OAuth (needed by some hosted MCP clients) is out of scope for now: see Consequences.

### Tools

Every tool is annotated. All but one are `readOnlyHint: true`.

| Tool | Backed by | Annotations | Notes |
|---|---|---|---|
| `list_services` | `CatalogService/ListServices` | read-only | name, tier, owners, dependencies |
| `get_service` | `CatalogService/GetService` | read-only | the catalog entry as YAML |
| `get_brief` | `InvestigationService/GetBrief` | read-only | `service`, `since` (default `1h`). No LLM, no cost |
| `list_investigations` | `InvestigationService/ListInvestigations` | read-only | `limit` (default 10, max 50), optional `service` and `status` filters applied in the tool |
| `get_investigation` | `InvestigationService/GetInvestigation` | read-only | mitigation and report as markdown (`report.MitigationMarkdown`, `report.Markdown`); steps only with `include_steps: true`, capped |
| `wait_investigation` | `InvestigationService/WatchInvestigation` | read-only | blocks until the mitigation is published or the investigation finishes, at most `timeout_s` (default 60, max 300), sending MCP progress notifications per step; then returns what `get_investigation` returns |
| `list_actions` | `ActionService/ListActions` | read-only | proposed actions show the CLI and Slack approval paths, not an MCP one |
| `get_action` | `ActionService/GetAction` | read-only | |
| `start_investigation` | `InvestigationService/StartInvestigation` | `readOnlyHint: false`, `destructiveHint: false`, `idempotentHint: false`, `openWorldHint: true` | `service`, `description`, `title`. Spends LLM tokens and posts to Slack. Returns the ID right away; pair it with `wait_investigation` |

Not exposed, on purpose:

- **`ApproveAction` / `RejectAction`.** Approval stays with a named human in the CLI or Slack. `get_action` tells the client how a human approves.
- **Catalog and connector writes** (`UpsertService`, `DeleteService`, `ImportCatalog`, `CreateConnector`, `DeleteConnector`). These are setup, done from the CLI or CI.
- **`TestConnector` and `DiscoverServices`.** Setup tools that hit providers; little value in an incident.

### Results

- Each tool returns **markdown text** for the model and the same data as **`structuredContent`** (protojson of the response), so clients that render structured results can. There is no output schema yet: protojson's shape is the API's, documented in `proto/stromboli/v1`.
- Results are capped (64 KB of text; steps truncated with a "use `include_steps` with `after_seq`" hint), like the agent's own tool output.
- Everything returned already went through stromboli's redaction ([0006](0006-suggest-dont-execute.md)): briefs and reports are built from redacted evidence.

### Audit

- Investigations started through MCP get `source: "mcp"` and the MCP client's name and version from `initialize` as `mcp_client` in their alert payload, so they show up as such in Slack and the API. The MCP server sends both as `Stromboli-Source` and `Stromboli-Client` headers on `StartInvestigation`, so this works the same over stdio, through the public API.
- Every tool call is logged with `slog` (tool, client, duration, error), never with its arguments.

### Later, not in this decision

- **The agent's evidence tools** (`log_patterns`, `query_logs`, `get_metrics`, `recent_deploys`…) as MCP tools, reusing the table in `internal/agent/tools.go` and its redaction and size caps. Useful, but it turns stromboli into a query gateway for every connector; it needs its own decision and probably its own token scope.
- **Resources** (`stromboli://investigations/{id}`, `stromboli://runbooks/{path}`) and a **prompt** (`triage_incident`) once clients use them consistently.

## Consequences

- An engineer can pull stromboli's brief and report into the tool they already use, and start an investigation from it, with the same guarantees as the API.
- The MCP surface is a strict subset of the API, enforced by building it on the Connect interfaces; new RPCs are not exposed until a tool is added for them.
- No new way to change production: the only side-effecting tool starts an investigation, which webhooks and the CLI can already do.
- `buf breaking` does not cover tool names and schemas. Changing a tool breaks MCP clients, so tool names, annotations and input schemas are pinned by a golden file (`internal/mcpserver/testdata/tools.golden.json`, refreshed with `go test ./internal/mcpserver -update`).
- Hosted MCP clients that require OAuth cannot connect until stromboli has user authentication (see the open question on UI authentication in the [ADR index](README.md)). Clients that send a bearer header (Claude Code, Cursor, `stromboli mcp` over stdio) work from day one.
