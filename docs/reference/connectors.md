# Connectors reference

A connector is a named, configured data source. Its non-secret settings are stored in plain JSON; its secret settings are encrypted with AES-256-GCM under `STROMBOLI_SECRET_KEY` and never returned by the API (`ListConnectors` shows a secret key with an empty value, the CLI as `••••`).

Create one with `stromboli connector add <name> <kind> key=value...` or `ConnectorService/CreateConnector`. Unknown keys and missing required keys are rejected. Catalog entries refer to connectors by name.

## Kinds

**Bold** keys are required; keys marked 🔒 are secret.

| Kind | Role | Keys | Through |
|---|---|---|---|
| `loki` | logs | **`url`**, `tenant_id`, `username`, `password` 🔒, `bearer_token` 🔒 | Loki HTTP API |
| `axiom` | logs | `org_id`, `message_field`, `mcp_url`, `client_metadata_url`; logs in with OAuth | Axiom's remote MCP server |
| `cloudwatch` | logs | `region`, `access_key_id`, `secret_access_key` 🔒, `session_token` 🔒, `endpoint`, `log_group_prefix`, `service_field` | AWS SDK |
| `gcp_logging` | logs | **`project`**, `credentials_json` 🔒 | Google's remote MCP server `logging.googleapis.com/mcp` |
| `prometheus` | metrics | **`url`**, `tenant_id`, `username`, `password` 🔒, `bearer_token` 🔒, `service_label` | Prometheus HTTP API |
| `fly` | metrics, Fly machine list | **`org`**, **`token`** 🔒, `url` | Fly managed Prometheus, Machines API |
| `gcp_monitoring` | metrics | **`project`**, `credentials_json` 🔒 | Google's remote MCP server `monitoring.googleapis.com/mcp` |
| `github` | deploys and commits | `token` 🔒, `base_url`, `mcp_url` | GitHub's remote MCP server; REST API for deployments and without a token |
| `vercel` | logs, deploys | **`team_id`**, `mcp_url`, `client_metadata_url`; logs in with OAuth | Vercel's remote MCP server |
| `fly_machines` | machine actions | **`org`**, **`token`** 🔒, `url` | Machines API |

Common keys:

| Key | |
|---|---|
| `tenant_id` | sent as `X-Scope-OrgID` (Loki, Mimir, Cortex) |
| `username`, `password` | HTTP basic auth |
| `bearer_token` | `Authorization: Bearer` |

## Log connectors

| Kind | `logs.selector` | Error filter applies to | `catalog discover` proposes |
|---|---|---|---|
| `loki` | LogQL stream selector: `{service_name="api"}` | the log line | one service per `service_name`, `service`, `app`, `job` or `container` label value |
| `axiom` | start of an APL query: `['logs']` or `['logs'] \| where ['service.name'] == "api"` | the message field (`message_field`, else detected: `message`, `msg`, `body`, `log`…) | one service per `service.name`, `service` or `app` value in each log dataset, or one per dataset |
| `cloudwatch` | log group(s), optionally followed by Insights commands: `/ecs/api` or `/ecs/prod, /ecs/worker \| filter service = "api"` | `@message` | one service per log group, named after its last path segment; with `service_field`, one per value of that field (groups limited to `log_group_prefix`) |
| `gcp_logging` | Logging query language filter: `resource.type="cloud_run_revision" AND resource.labels.service_name="api"` | `textPayload` and `jsonPayload.message`, plus `severity>=ERROR` and `httpRequest.status>=500` | one service per Cloud Run service or job, function, Cloud Scheduler job, Cloud Tasks queue, Pub/Sub subscription or topic seen in the last hour |
| `vercel` | a project name or ID, optionally followed by `environment=production` or `environment=preview`: `web environment=production` | the message, prefixed with its level (`level=error …`). Counts with a filter stop at 500 lines | one service per project of the team |

Filters are case-insensitive RE2 regular expressions on the message. The default error filter matches `error`, `err`, `fatal`, `panic`, `exception`, `level=error|fatal|crit`, `"level": "error|fatal|critical"`, `status=5xx`, `timeout` and `timed out`.

JSON log lines (Fly's log shipper, Vector, most structured loggers) are reduced to their message and level, so patterns are not split by host or region. Cloud Logging has no count tool, so error counts there stop at 5000.

### loki

`url` is the Loki base URL (`http://loki:3100`).

### axiom

Axiom's MCP server, logging in with OAuth in a browser (see [MCP connections](#mcp-connections)). `mcp_url` defaults to `https://mcp.axiom.co/mcp`; set `https://mcp.eu.axiom.co/mcp` to keep query results in the EU. The login can reach every organization of your Axiom user: `org_id` picks one, else the login's default is used. stromboli calls `listDatasets` and `queryDataset`; Axiom does not mark `queryDataset` read-only because it can save a result snapshot (`saveResults`), which stromboli never asks for, so it is allowed by name.

### cloudwatch

Without `access_key_id`/`secret_access_key`, the server's default AWS credential chain is used. Required IAM permissions: `logs:DescribeLogGroups`, `logs:StartQuery`, `logs:GetQueryResults`, `logs:StopQuery`. `endpoint` overrides the AWS endpoint.

### gcp_logging

Without `credentials_json` (a service account key; pass `credentials_json=@key.json`), Application Default Credentials are used. The principal needs `roles/mcp.toolUser` and `roles/logging.viewer`. Calls are billed to `project`.

## Metrics connectors

| Kind | `metrics.selectors` | Presets | `catalog discover` proposes |
|---|---|---|---|
| `prometheus` | `job="api"`, `service="api"` | none | one service per value of the first of `service_name`, `service`, `app`, `job` with values, or of `service_label` |
| `fly` | `app="checker"` | [fly](metric-presets.md#fly) | one service per `app` label value |
| `gcp_monitoring` | `subscription_id="…"`, `queue_id="…"`, `service_name="…"` | [gcp_monitoring](metric-presets.md#gcp_monitoring) | one service per Pub/Sub subscription, Cloud Tasks queue and Cloud Run service with data in the last hour |

For metrics connectors, discovery also returns existing services without metrics, as their full entry with `metrics` filled in.

In PromQL passed to `get_metrics`, `$selector` expands to the service's matchers: `sum by (instance) (rate(http_requests_total{$selector, code=~"5.."}[1m]))`.

### prometheus

Works with anything that speaks the Prometheus HTTP API: Prometheus, Mimir, Thanos, VictoriaMetrics, Grafana Cloud.

### fly

`org` is the organisation slug (`fly orgs list`; `personal` for a personal org). Metrics are read from `https://api.fly.io/prometheus/<org>` (`url` overrides the base). Tokens from `fly tokens create` (`FlyV1 …`) are sent with the `FlyV1` scheme, others as `Bearer`. A read-only org token (`fly tokens create readonly <org>`) is enough. The same token lists an app's machines for the brief, unless the service has a `fly_machines` connector.

### gcp_monitoring

Credentials as for [`gcp_logging`](#gcp_logging); the principal needs `roles/mcp.toolUser` and `roles/monitoring.viewer`.

## Deploy connectors

### github

| Key | |
|---|---|
| `token` | a read-only token. Without it, only public repositories work, through the REST API |
| `base_url` | REST API base for GitHub Enterprise Server (default `https://api.github.com`). With `base_url` and no `mcp_url`, everything uses REST |
| `mcp_url` | MCP server URL (default `https://api.githubcopilot.com/mcp/`) |

With a token, stromboli uses GitHub's remote MCP server with the `repos` and `actions` toolsets in read-only mode. GitHub deployments (`deploys.environment`) have no MCP tool and always use REST.

What a deploy is depends on the service's [`deploys`](catalog.md#deploys): completed runs of `workflow`, deployments to `environment`, or commits on `branch`.

### vercel

| Key | |
|---|---|
| `team_id` | the team's slug (as in `vercel.com/<slug>`) or ID (`team_…`). `connector test` checks that the login can read its projects |
| `mcp_url` | MCP server (default `https://mcp.vercel.com/`) |
| `client_metadata_url` | stromboli's client ID metadata document, if you host one. Without it, stromboli registers itself with Vercel at login |

There is no token to pass: `connector add` logs in to Vercel in a browser. The login has the access of your Vercel user; stromboli may only call `list_projects`, `list_deployments` and `get_runtime_logs`, all marked read-only by Vercel.

Logs are the project's runtime logs (`get_runtime_logs`): 100 lines per call, as far back as your Vercel plan keeps them. Deploys are the deployments of the service's `deploys.project` to `deploys.environment` (`production` by default, or `preview`), optionally only of `deploys.branch`. The commit SHA, message and author come from the Git metadata Vercel records (GitHub, GitLab or Bitbucket). Vercel logs are read through a log store: see [Connect Vercel](../how-to/connect-vercel.md).

## Action connectors

### fly_machines

Holds a token that can act on machines, e.g. `fly tokens create deploy -a <app>`. Used only by approved [machine actions](machine-actions.md) and to list machines. Keep it separate from the read-only `fly` connector.

## MCP connections

For connectors that go through an MCP server (`gcp_logging`, `gcp_monitoring`, `github` with a token, `vercel`, `axiom`):

- each connector may call only an allow-listed set of tools; any other tool the server offers is refused;
- for servers that annotate tools (Google Cloud, GitHub, Vercel, Axiom), connecting fails unless every allowed tool is marked `readOnlyHint`, except a tool named as used read-only (Axiom's `queryDataset`, without `saveResults`);
- sessions are reused per connector and closed on shutdown.

`vercel` and `axiom` log in with OAuth: `connector add` opens a browser once, and the server keeps the connector working with the refresh token, saving it when it rotates. When the login expires or is revoked, run `stromboli connector login <name>`. See [ADR 0022](../adr/0022-oauth-for-remote-mcp-servers.md).

See [Data access](../explanation/data-access.md) for why.
