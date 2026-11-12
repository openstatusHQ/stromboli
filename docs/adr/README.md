# Architecture decisions

Why stromboli is built the way it is. Each record explains one decision: its context, what we decided and the consequences ([0001](0001-record-architecture-decisions.md)).

To record a new decision, copy [`template.md`](template.md) to the next number, open it as **Proposed** in a PR, and add it to the list below. Don't rewrite an accepted record: supersede it with a new one.

| # | Decision | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-self-hosted-one-instance-per-organisation.md) | Self-hosted, one instance per organisation | Accepted |
| [0003](0003-service-catalog-owned-by-stromboli.md) | The service catalog is owned by stromboli | Accepted |
| [0004](0004-postgres-only-storage.md) | Postgres as the only storage | Accepted |
| [0005](0005-go-binary-api-first-connectrpc.md) | A single Go binary, API first with ConnectRPC | Accepted |
| [0006](0006-suggest-dont-execute.md) | Suggest, don't execute | Accepted |
| [0007](0007-query-logs-on-demand.md) | Query data on demand, store only the evidence | Accepted |
| [0008](0008-log-backend-order.md) | Log backends: Loki first | Accepted |
| [0009](0009-mitigate-first-investigate-second.md) | Mitigate first, investigate second | Accepted |
| [0010](0010-investigation-flow.md) | Investigation flow: deterministic brief, then a single agent | Accepted |
| [0011](0011-mcp-first-connectors.md) | MCP first, else the provider's API | Accepted |
| [0012](0012-metrics-through-prometheus-api.md) | Metrics through the Prometheus HTTP API | Accepted |
| [0013](0013-approved-fly-machine-actions.md) | Approved machine actions, Fly.io only | Accepted |
| [0014](0014-slack-approval-buttons.md) | Approve actions from Slack | Accepted |
| [0015](0015-cli-follows-clig.md) | The CLI follows clig.dev | Accepted |
| [0016](0016-multi-provider-llm-per-task-models.md) | Any LLM provider, one model per task | Accepted |
| [0017](0017-multi-role-investigation.md) | Multi-role investigation | Proposed |
| [0018](0018-memory-notes.md) | Memory notes with a lifecycle | Proposed |
| [0019](0019-evaluation-harness.md) | Evaluation by replaying recorded incidents | Proposed |
| [0020](0020-mit-license.md) | MIT license | Accepted |
| [0021](0021-mcp-server.md) | stromboli as an MCP server | Proposed |
| [0022](0022-oauth-for-remote-mcp-servers.md) | OAuth for remote MCP servers | Proposed |

## Open questions

Not decided yet; each becomes an ADR once it is.

- Outputs beyond Slack: Discord, Teams, the openstatus dashboard, openstatus incident notes and status report drafts?
- UI authentication in v1: local users only, or OIDC from the start?
- Telemetry: anonymous, opt-out usage stats to learn which connectors to build next?
