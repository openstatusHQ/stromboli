# opensre

[![ci](https://github.com/openstatushq/opensre/actions/workflows/ci.yml/badge.svg)](https://github.com/openstatushq/opensre/actions/workflows/ci.yml) [![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**An open-source AI SRE agent you host yourself.** When an alert fires, opensre gathers the context, tells you how to stop the bleeding, then finds the root cause, and posts all of it to Slack.

- 🩹 **Mitigation first.** It proposes a concrete fix (rollback, restart, scale up…) within a few tool calls, before it starts digging for the root cause.
- 🔍 **Then the root cause.** It tests hypotheses against your logs, metrics and deploys, and writes a structured report.
- 🔒 **Read-only by default.** It suggests and never acts on its own. The one opt-in exception: on Fly.io it can run a machine action (restart, clone, cordon…) after a named human approves it ([Machine actions](#machine-actions)).
- 🧠 **Any LLM.** Claude by default; OpenAI, Gemini, DeepSeek, Ollama, OpenRouter or Vercel AI Gateway work too ([Models](#models)).
- 📦 **No data ingestion.** Logs and metrics are queried on demand where they already live. opensre only stores the aggregated evidence each investigation used.

See [docs/adr](docs/adr) for the design decisions behind opensre and what is planned next.

## Contents

- [Quickstart](#quickstart)
- [Slack](#slack)
- [How an investigation works](#how-an-investigation-works)
- [The service catalog](#the-service-catalog)
- [Connecting your data](#connecting-your-data): [logs](#log-connectors), [metrics](#metric-connectors), [Google Cloud](#google-cloud), [Fly.io](#flyio)
- [Machine actions](#machine-actions) and [approving from Slack](#approve-from-slack)
- [Models](#models)
- [Configuration](#configuration)
- [CLI tips](#cli-tips)
- [API](#api) and [MCP server](#mcp-server)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Quickstart

### 1. Start opensre

```sh
cp .env.example .env   # fill in OPENSRE_SECRET_KEY, OPENSRE_API_TOKEN and an LLM key (ANTHROPIC_API_KEY...)
docker compose up -d   # opensre + Postgres on http://localhost:8080
```

Generate the secret key with `openssl rand -base64 32`. Slack is optional: [create the app from its manifest](#slack) to get reports there.

Already have Postgres? Run the image alone with `OPENSRE_DB_URL=postgres://...`. Migrations run on startup. Binaries for Linux, macOS and Windows are on the [releases page](https://github.com/openstatushq/opensre/releases).

Running it for real? Read [Self-hosting](docs/self-hosting.md) for the production checklist (TLS, tokens, backups) and how to upgrade.

Or deploy opensre and Postgres to Railway in one command, with the secrets generated for you. The setup is code, in [`.railway/railway.go`](.railway/railway.go) ([details](docs/deploy/railway.md)):

```sh
railway login
ANTHROPIC_API_KEY=sk-ant-... deploy/railway/setup.sh
```

### 2. Connect your logs, metrics and deploys

opensre is configured with its CLI (the same binary, so `docker compose exec opensre opensre ...` works too):

```sh
export OPENSRE_URL=http://localhost:8080 OPENSRE_API_TOKEN=...

opensre connector add loki loki url=http://loki:3100   # your log store
opensre connector add github github token               # deploys and commits; prompts for the token
```

Loki is just an example: see [Connecting your data](#connecting-your-data) for Axiom, CloudWatch, Google Cloud, Prometheus and Fly.io.

On Fly.io, a **read-only** org token is enough for metrics, the machine list and catalog discovery. It cannot deploy, restart or read secrets:

```sh
fly tokens create readonly <org-slug>                    # prints FlyV1 fm2_...; or pass it inline:
opensre connector add fly fly org=<org-slug> token="$(fly tokens create readonly <org-slug>)"
```

`<org-slug>` is in `fly orgs list` (`personal` for a personal org). Revoke it later with `fly tokens list` and `fly tokens revoke <id>`.

### 3. Describe your services

opensre can propose a catalog from what it finds in your logs or Fly metrics. Review the file, add owners, dependencies and runbooks, then import it:

```sh
opensre catalog discover loki > services.yaml   # or: discover fly, one service per Fly app
opensre catalog import services.yaml --runbooks runbooks/
```

### 4. Try it

```sh
opensre brief api --since 1h                                  # the context opensre gathers (no LLM, free)
opensre investigate api --description "5xx on /v1/monitors"   # a full investigation, streamed live
```

### 5. Send it your alerts

| Source | Endpoint |
|---|---|
| openstatus (webhook notification with header `Authorization: Bearer <token>`) | `POST /webhooks/openstatus` |
| Alertmanager | `POST /webhooks/alertmanager?token=<token>` |
| Grafana alerting | `POST /webhooks/grafana?token=<token>` |
| Anything else, as `{"service","title","fingerprint","details"}` | `POST /webhooks/generic` |

That's it: the next alert starts an investigation on its own.

## Slack

opensre posts one message per investigation (the mitigation first, then the report) and replies in its thread. The app only needs the `chat:write` scope, and it is described in [`deploy/slack/manifest.json`](deploy/slack/manifest.json):

1. **Create the app.** [Create it from the manifest](https://api.slack.com/apps?new_app=1&manifest_json=%7B%22display_information%22%3A%7B%22name%22%3A%22opensre%22%2C%22description%22%3A%22AI%20SRE%20agent%3A%20posts%20the%20mitigation%20and%20root%20cause%20of%20your%20alerts.%22%2C%22background_color%22%3A%22%231f2937%22%7D%2C%22features%22%3A%7B%22bot_user%22%3A%7B%22display_name%22%3A%22opensre%22%2C%22always_online%22%3Atrue%7D%7D%2C%22oauth_config%22%3A%7B%22scopes%22%3A%7B%22bot%22%3A%5B%22chat%3Awrite%22%5D%7D%7D%2C%22settings%22%3A%7B%22interactivity%22%3A%7B%22is_enabled%22%3Atrue%2C%22request_url%22%3A%22https%3A%2F%2Fopensre.example.com%2Fslack%2Finteractions%22%7D%2C%22org_deploy_enabled%22%3Afalse%2C%22socket_mode_enabled%22%3Afalse%2C%22token_rotation_enabled%22%3Afalse%7D%7D) and pick your workspace. Or go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From a manifest** and paste the file. Before creating it, replace `opensre.example.com` in `request_url` with your opensre's public URL (or remove the `interactivity` block if you won't use [Apply/Reject buttons](#approve-from-slack)).
2. **Install it.** **Install App** → **Install to Workspace**, then copy the **Bot User OAuth Token** (`xoxb-...`).
3. **Invite the bot** to the channel: `/invite @opensre` in it.
4. **Configure opensre** and restart it:

   ```sh
   SLACK_BOT_TOKEN=xoxb-...
   SLACK_CHANNEL=#incidents             # a name or a channel ID (C0123...)
   SLACK_SIGNING_SECRET=...             # optional: Basic Information → Signing Secret, enables the buttons
   SLACK_APPROVERS=U0123,U0456          # optional: who may click Apply/Reject (profile → Copy member ID)
   ```

Check it with `opensre investigate <service> --description "testing Slack"`: the message should appear in the channel. `not_in_channel` in the logs means step 3 is missing; `invalid_auth`, a wrong token.

## How an investigation works

```
alert ──▶ 1. match ──▶ 2. brief ──▶ 3. mitigate ──▶ 4. investigate ──▶ 5. report
                        (no LLM)    (posted now)    (read-only tools)   (Slack + API)
```

1. **Match.** The alert is matched to a catalog service (openstatus monitor ID, `service`/`app` labels). Repeats are merged into the running investigation, or into one that finished within `OPENSRE_DEDUPE_WINDOW`, unless the alert resolved since (openstatus `recovered`, Alertmanager `resolved`) or one of its machine actions succeeded: the alert firing again is then a new incident. Merged alerts and webhooks with a wrong token are logged.
2. **Brief (no LLM).** opensre collects what an on-call engineer would look at first:
   - the service, its dependencies and blast radius
   - log error patterns, compared with the preceding window of the same length
   - per-instance metrics (memory, CPU, throttling, OOM kills, request rate, 5xx, p95 latency), against the same baseline
   - recent deploys or commits of the service and its dependencies
   - its runbooks and similar past investigations
   - on Fly.io: the app's machines and the exact Fly command, with its undo, for each mitigation
3. **Mitigate.** What matters most is how fast user impact stops, not how fast the bug is understood (as in [Google's SRE practice](https://cloud.google.com/blog/topics/developers-practitioners/how-google-sres-use-gemini-cli-to-solve-real-world-outages)). The model picks one mitigation from a fixed set: `rollback`, `restart`, `scale_up`, `drain`, `disable_feature`, `block_traffic`, `failover`, `degrade` or `none`. It gives the target, the exact command, how to check it worked, how to undo it, and the risk (`safe`, `reversible`, `destructive`). It is posted to Slack right away (with a thread reply so on-call is notified) and its time is stored as `mitigated_at`. After `OPENSRE_MITIGATION_TOOL_CALLS` tool calls, the model is told to commit to one, and it cannot submit a report without one.
4. **Investigate.** The model looks for the root cause with read-only tools:

   | Area | Tools |
   |---|---|
   | Logs | `log_patterns`, `query_logs`, `log_timeline` |
   | Metrics | `get_metrics` (a preset such as `memory_used_percent`, or any PromQL), `list_metrics` |
   | Deploys | `recent_deploys`, `get_commit` |
   | Catalog | `get_service`, `list_services` |
   | Knowledge | `read_runbook`, `search_past_investigations` |

   Tool output is aggregated, size-capped and redacted (tokens, passwords, emails) before the model sees it. If the evidence shows the mitigation won't help (a restart can't fix a bad config push), the model revises it.
5. **Report.** Likely cause, confidence, evidence, what was ruled out, the mitigation in light of the cause, the long-term fix and next steps, posted to Slack and the API. Every brief, tool call and model turn is kept as an audit trail.

## The service catalog

The catalog tells opensre what your services are and where their data lives:

```yaml
- name: api
  description: Public REST API
  owners: ["@team-backend"]
  tier: 1
  depends_on: [db-proxy]
  logs:
    source: loki                       # connector name
    selector: '{service_name="api"}'
  deploys:
    source: github                     # connector name
    repo: acme/api
    workflow: deploy.yml               # or environment: production; or neither = commits
  metrics:
    source: fly                        # connector name (fly or prometheus)
    selectors: ['app="api"']           # PromQL label matchers, ANDed
  runbooks: [runbooks/api-5xx.md]
  monitors: ["openstatus:1234"]        # alert → service mapping
  runtime: fly.io                      # or a fly: block, see Machine actions
```

The catalog lives in opensre's database. YAML is its import/export format, so you can keep it in git and sync it from CI:

```sh
opensre catalog import services.yaml --runbooks runbooks/ --prune --yes   # --prune makes the file authoritative
```

A full example is in [`examples/catalog`](examples/catalog).

## Connecting your data

| What | Connectors |
|---|---|
| Logs | [`loki`, `axiom`, `cloudwatch`, `gcp_logging`](#log-connectors) |
| Metrics | [`fly`, `prometheus`, `gcp_monitoring`](#metric-connectors) |
| Deploys and commits | `github` |
| Fly.io machines | `fly`, `fly_machines` ([Fly.io](#flyio)) |

### MCP first

opensre calls a provider through the MCP server the provider hosts, when there is one. Otherwise it calls the provider's API directly, without adding a CLI or local process just to get MCP. opensre is the MCP client: it calls the tools from code and turns their output into compact evidence. The model never talks to these servers directly.

- **Allow-list.** Each connector names the few tools it may call (`internal/connector/mcpconn`). Any other tool the server offers is refused, including `deploy`, `delete` and `exec`.
- **Read-only check.** For servers that annotate their tools (Google Cloud, GitHub), connecting fails unless every allowed tool is marked `readOnlyHint`.
- **Sessions are reused** per connector and closed on shutdown.

| Provider | Through | Not covered by MCP (REST) |
|---|---|---|
| Google Cloud: `gcp_logging`, `gcp_monitoring` | Google's remote MCP servers (`logging.googleapis.com/mcp`, `monitoring.googleapis.com/mcp`) | none |
| GitHub: `github` with a token | GitHub's remote MCP server, toolsets `repos,actions`, read-only mode | Deployments (`deploys.environment`), and token-less use |
| Fly.io, Loki, Prometheus, CloudWatch | their APIs: the Machines API, Fly's managed Prometheus, the Loki and Prometheus HTTP APIs, the AWS SDK | no hosted MCP server: see [Not on MCP](#not-on-mcp) |
| Axiom | REST API for now | see [Not on MCP](#not-on-mcp) |

#### Not on MCP

- **Fly.io** offers MCP only through `flyctl mcp server`, a local process that runs flyctl commands and serves all of them (destroy, exec, secrets) without marking any read-only. opensre calls the Machines API directly instead.
- **CloudWatch:** AWS's MCP server (`awslabs.cloudwatch-mcp-server`) is a local Python process, so opensre uses the AWS SDK.
- **Loki and Prometheus** have no hosted MCP server. Grafana's `mcp-grafana` needs a Grafana instance in front of them.
- **Axiom** hosts an MCP server (`mcp.axiom.co`). Its tool list needs credentials even to read, so its tools have not been checked yet. Until then, opensre uses its REST API.

### Log connectors

| Kind | Add it | `logs.selector` | `catalog discover` proposes |
|---|---|---|---|
| `loki` | `connector add loki loki url=http://loki:3100` (+ `tenant_id`, `username`, `password`, `bearer_token`) | LogQL stream selector: `{service_name="api"}` | one service per `service_name` / `service` / `app` / `job` / `container` label value |
| `axiom` | `connector add axiom axiom token=xaat-...` (+ `url` for other regions, `org_id` for personal tokens, `message_field`) | Start of an APL query: `['logs']` or `['logs'] \| where ['service.name'] == "api"` | one service per `service.name` / `service` / `app` value in each log dataset, or one per dataset |
| `cloudwatch` | `connector add cw cloudwatch region=eu-west-1` (+ `access_key_id`, `secret_access_key`, `session_token`, `endpoint`) | Log group(s), optionally followed by Insights commands: `/ecs/api` or `/ecs/prod, /ecs/worker \| filter service = "api"` | one service per log group, named after its last path segment |
| `gcp_logging` | `connector add gcp-logs gcp_logging project=<project-id>` (+ `credentials_json=@key.json`) | Logging query language filter: `resource.type="cloud_run_revision" AND resource.labels.service_name="api"` | one service per Cloud Run service or job, function, Cloud Scheduler job, Cloud Tasks queue, Pub/Sub subscription or topic seen in the last hour |

Error filters are RE2 regexes applied to the log line: Loki's line, Axiom's message field (detected from `message`, `msg`, `body`, `log`, ... or set with `message_field`), CloudWatch's `@message`, and Cloud Logging's `textPayload` and `jsonPayload.message`. On Cloud Logging, the error filter also matches `severity>=ERROR` and `httpRequest.status>=500`. Cloud Logging has no count tool, so error counts there stop at 5000.

Without `access_key_id`/`secret_access_key`, the CloudWatch connector uses the default AWS credential chain of the opensre server (environment, shared config, IAM role). It needs `logs:DescribeLogGroups`, `logs:StartQuery`, `logs:GetQueryResults` and `logs:StopQuery`.

### Metric connectors

| Kind | Add it | `metrics.selectors` | Presets |
|---|---|---|---|
| `fly` | `connector add fly fly org=<org-slug> token="$(fly tokens create readonly <org-slug>)"` | `app="checker"` | yes, see [Fly.io](#machine-metrics) |
| `prometheus` | `connector add prom prometheus url=http://prometheus:9090` (+ `tenant_id`, `username`, `password`, `bearer_token`, `service_label`) | `job="api"`, `service="api"` | none: the agent uses `list_metrics` and writes PromQL |
| `gcp_monitoring` | `connector add gcp-metrics gcp_monitoring project=<project-id>` (+ `credentials_json=@key.json`) | `subscription_id="checker-ams"`, `queue_id="alerting"`, `service_name="api"` | yes, see [Google Cloud](#google-cloud) |

`prometheus` works with anything speaking the Prometheus HTTP API (Prometheus, Mimir, Thanos, VictoriaMetrics, Grafana Cloud). In a PromQL query, `$selector` expands to the service's matchers, e.g. `sum by (instance) (rate(http_requests_total{$selector, code=~"5.."}[1m]))`. Each series comes back as min, max, avg, last, its average over the baseline window and 12 points, capped at 10 series.

`opensre catalog discover <metrics-connector>` proposes `metrics` for every app it finds (one per `app` label value on Fly; the first of `service_name`, `service`, `app`, `job` with values on Prometheus, or `service_label`). Services already in the catalog without metrics come back as their full entry with `metrics` filled in, so `opensre catalog import` only adds the metrics.

### Google Cloud

Both GCP connectors go through Google's remote MCP servers using PromQL and the Logging query language. Without `credentials_json` (a service account key), they use Application Default Credentials: `GOOGLE_APPLICATION_CREDENTIALS`, workload identity, or `gcloud auth application-default login`. The principal needs `roles/mcp.toolUser`, `roles/logging.viewer` and `roles/monitoring.viewer`. Calls are billed to `project`.

`gcp_monitoring` presets depend on the resource label in the service's selectors. A selector with `subscription_id` gets the Pub/Sub presets, `queue_id` gets Cloud Tasks, and `service_name` gets Cloud Run:

| Product | Presets |
|---|---|
| Pub/Sub | `pubsub_backlog_messages`, `pubsub_oldest_unacked_seconds`, `pubsub_acks_per_second`, `pubsub_sent_per_second`, `pubsub_expired_ack_deadlines_per_second`, `pubsub_dead_letter_per_second`, `pubsub_nacks` |
| Cloud Tasks | `tasks_queue_depth`, `tasks_attempts_per_second` (by response code), `tasks_attempt_delay_p95_seconds` |
| Cloud Run | `http_requests_per_second`, `http_5xx_per_second`, `http_p95_seconds`, `instances`, `cpu_utilization_p95_percent`, `memory_utilization_p95_percent`, `startup_latency_p95_seconds` (per revision) |

`list_metrics` lists the project's active metrics under their PromQL names (`pubsub_googleapis_com:subscription_num_undelivered_messages`). `catalog discover` proposes one service per Pub/Sub subscription, Cloud Tasks queue and Cloud Run service with data in the last hour.

### Fly.io

#### Machine metrics

Fly collects the metrics of every machine in the organisation into a [managed Prometheus](https://fly.io/docs/monitoring/metrics/) at `https://api.fly.io/prometheus/<org-slug>`, so there is nothing to deploy:

```sh
opensre connector add fly fly org=<org-slug> token="$(fly tokens create readonly <org-slug>)"
opensre catalog discover fly > fly.yaml && opensre catalog import fly.yaml
```

Tokens from `fly tokens create` are sent with the `FlyV1` scheme, others (from `fly auth token`) as `Bearer`. The `fly` connector comes with presets, per machine (`instance`) and `region`:

| Preset | From |
|---|---|
| `memory_used_percent`, `memory_used_bytes`, `swap_used_bytes` | `fly_instance_memory_mem_total` - `mem_available`, `swap_total` - `swap_free` |
| `oom_killed` | `fly_instance_exit_oom` |
| `cpu_percent`, `cpu_throttled_seconds_per_second`, `load_average` | `fly_instance_cpu` (non-idle share), `fly_instance_cpu_throttle`, `fly_instance_load_average` |
| `disk_used_percent`, `volume_used_percent` | `fly_instance_filesystem_blocks*`, `fly_volume_used_pct` |
| `net_recv_bytes_per_second`, `net_sent_bytes_per_second` | `fly_instance_net_*_bytes` |
| `http_requests_per_second`, `http_5xx_per_second`, `http_p95_seconds`, `concurrency` | `fly_app_http_responses_count`, `fly_app_http_response_time_seconds`, `fly_app_concurrency` |
| `instances_up` | `fly_instance_up` |

The brief shows memory, CPU, throttling, OOM kills, root filesystem and volume usage, request rate, 5xx and p95 latency; the agent can query the rest with `get_metrics`.

#### Logs

Fly has no API to query past logs. The whole organisation's logs are published on a NATS stream, and Fly's [log shipper](https://github.com/superfly/fly-log-shipper) forwards them to a log store. Point it at Loki, Axiom or CloudWatch, then add that store as a connector:

```sh
fly launch --image flyio/log-shipper:latest --no-public-ips --name log-shipper   # set internal_port = 8686
fly secrets set -a log-shipper ORG=<org-slug> ACCESS_TOKEN="$(fly tokens create readonly <org-slug>)"
# then one of:
fly secrets set -a log-shipper LOKI_URL=https://loki.example.com LOKI_USERNAME=... LOKI_PASSWORD=...
fly secrets set -a log-shipper AXIOM_TOKEN=xaat-... AXIOM_DATASET=fly-logs
fly secrets set -a log-shipper AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_REGION=eu-west-1 CLOUDWATCH_LOG_GROUP_NAME=/fly/logs
```

| Store | Connector | Discovered selector, one service per Fly app |
|---|---|---|
| Loki | `connector add loki loki url=...` | `{fly_app_name="checker"}` |
| Axiom | `connector add axiom axiom token=...` | `['fly-logs'] \| where ['fly.app.name'] == "checker"` |
| CloudWatch | `connector add cw cloudwatch region=eu-west-1 log_group_prefix=/fly/ service_field=fly.app.name` | `/fly/logs \| filter fly.app.name = "checker"` |

The shipper writes JSON events (`message`, `log.level`, `fly.app.name`, `fly.region`, `host`). opensre shows the `message` with its level, so pattern grouping isn't split by region or instance. The CloudWatch shipper writes every app to one log group, so `service_field` makes discovery split it per app with one Insights query (`log_group_prefix` keeps that query to the Fly group).

#### Mitigation commands

For a service with `runtime: fly.io` or a `fly:` block, the brief gets a **Fly.io** section. It lists the app's machines from the [Machines API](https://fly.io/docs/machines/api/), so the agent can name real machine IDs and regions (the `instance` label of the Fly metrics is the machine ID). It also gives the Fly command and its undo for each mitigation:

| Mitigation | Command | Undo |
|---|---|---|
| restart one machine | `fly machine restart <id> -a <app>` | none needed |
| restart every machine | `fly apps restart <app>` | none needed |
| scale_up, one more machine | `fly machine clone <id> -a <app> --region <region>` | `fly machine destroy <new-id> -a <app> --force` |
| scale_up, more machines / memory | `fly scale count <n>` / `fly scale memory <MB>` | the previous value |
| drain | `fly machine cordon <id>` (no traffic, keeps running) or `fly machine stop <id>` | `uncordon` / `start` |
| scale_up, more disk (full volume) | `fly volumes extend <vol-id> -s <GB> -a <app>` | none: volumes cannot shrink |
| free space | `fly ssh console -a <app> --machine <id>`, then delete or rotate data | none |
| rollback | `fly releases --image`, then `fly deploy --image <previous image>` | redeploy the bad image |

Machines with a volume are flagged and their volumes listed (ID, mount path, size), so a full volume can be extended by ID: a restart does not free space on a volume, and a clone would start with an empty one. The app name is `fly.app`, else the `app="..."` metrics selector, else the service name. The machines are listed with the `fly.actions` connector, else the `fly` metrics connector's token.

#### Health checks

Some failures never fire an alert until it is too late, like a volume filling up over days. With `OPENSRE_HEALTHCHECK_INTERVAL=1h`, opensre reads the metrics of every service with a `fly` metrics source once an hour (the first run is a minute after startup) and starts an investigation, with source `healthcheck`, when a machine or volume crosses a threshold over the last 15 minutes:

| Check | Threshold |
|---|---|
| `volume_used_percent` | last value ≥ 85% |
| `disk_used_percent` (root filesystem) | last value ≥ 90% |
| `memory_used_percent` | last value ≥ 90% |
| `cpu_percent` | 15-minute average ≥ 90% |

When everything is fine, it only logs a line. Each unhealthy service gets one investigation, with the findings in its alert payload, posted to Slack like any other. While the same checks keep failing, it is not investigated again for 6 hours. Without an LLM, findings are only logged.

## Machine actions

By default, every mitigation is text for a human to apply. For Fly.io services you can let opensre run some of them itself, always after a human approves.

**Turn it on:**

```sh
export OPENSRE_ACTIONS_ENABLED=true          # off by default; requires OPENSRE_API_TOKEN
opensre connector add fly-deploy fly_machines org=<org-slug> token="$(fly tokens create deploy -a <app>)"
```

```yaml
- name: checker
  fly:
    app: openstatus-checker            # defaults as above
    actions: fly-deploy                # fly_machines connector; without it, mitigations stay text
    allow: [restart, cordon, uncordon] # default: restart, clone, cordon, uncordon (stop and start are opt-in)
```

**What happens:**

1. **Proposed.** When the brief says actions can run for the service, the agent may attach a `machine_action` (`restart`, `clone`, `cordon`, `uncordon`, `stop` or `start`, plus machine IDs and, for `clone`, a region) to its mitigation. opensre checks it against the allow-list and the app's current machines (at most 10 per action; machines with a volume are never cloned) and stores it as **proposed**. A refused action is dropped, the mitigation stays a suggestion, and the agent is told why. Revising the mitigation supersedes the pending action.
2. **Approved, or not.** Nothing runs until a human approves it, before `OPENSRE_ACTION_TTL` (30m) runs out:

   ```sh
   opensre actions --status proposed
   opensre actions approve <action-id> --as alice --note "ok from the incident call"   # asks to confirm; --yes skips
   opensre actions reject <action-id> --as alice --note "the db is the cause"
   ```

3. **Run, safely.** The approval is checked again against the catalog and the machines, then claimed atomically, so an action runs at most once. It runs machine by machine. A restart, start or clone waits for the machine to reach `started` (60s) and stops at the first failure, so a rolling restart never takes down more than one machine that did not come back. The result lists what was done and how to undo it (for a clone, the `fly machine destroy` of the new machine).
4. **Recorded.** Every action keeps who approved or rejected it, when, their note, the result and the error (`opensre actions show <id>`, `ActionService`), and is posted in the investigation's Slack thread.

> [!NOTE]
> The approver name is recorded as given: anyone with the API token can approve. Use [Slack approval](#approve-from-slack) with `SLACK_APPROVERS` to restrict who can.

### Approve from Slack

With `SLACK_SIGNING_SECRET` set (and `OPENSRE_ACTIONS_ENABLED=true`), a proposed action shows **Apply** and **Reject** buttons in the investigation's Slack message instead of the CLI command. Apply asks to confirm, showing the risk and the undo. The click goes through the same `Approve`/`Reject` as the CLI, with the Slack user recorded as the approver (`@alice (Slack U0123)`). The outcome is posted in the thread and replaces the buttons. Expired or superseded actions lose their buttons on the next redraw, and a click on one is answered with why it cannot run.

1. In your Slack app, turn on **Interactivity & Shortcuts** with Request URL `https://<opensre>/slack/interactions` (already on if you created it from the [manifest](#slack)).
2. Set `SLACK_SIGNING_SECRET` (Basic Information → Signing Secret). Requests without a valid Slack signature, or older than 5 minutes, are refused.
3. Recommended: `SLACK_APPROVERS=U0123,U0456` limits the buttons to those Slack user IDs. Without it, anyone who can see the channel can approve.

Only machine actions have buttons. A text-only mitigation (rollback, failover…) is still applied by a human outside opensre.

## Models

opensre is not tied to one LLM vendor. As with the [AI SDK](https://ai-sdk.dev/docs/reference/ai-sdk-core/provider-registry), a model is `provider:model`, and each phase can use its own. A good split is a fast model for triage, where time to mitigation matters most, and a thorough one for the root cause:

```sh
OPENSRE_TRIAGE_MODEL=anthropic:claude-haiku-5-5     # until the mitigation is published
OPENSRE_TRIAGE_EFFORT=low
OPENSRE_INVESTIGATE_MODEL=openai:gpt-5              # root cause and report
OPENSRE_INVESTIGATE_EFFORT=high
```

The investigate model takes over the triage conversation once the mitigation is published, so it sees everything triage saw.

The simplest setup is [Vercel AI Gateway](https://vercel.com/ai-gateway): one key, every model. As in the AI SDK, an id like `openai/gpt-5` (no `provider:` prefix, a slash) goes through the gateway:

```sh
AI_GATEWAY_API_KEY=...
OPENSRE_TRIAGE_MODEL=anthropic/claude-haiku-5.5
OPENSRE_INVESTIGATE_MODEL=openai/gpt-5
```

| Provider | Model id | Configuration |
|---|---|---|
| Vercel AI Gateway | `openai/gpt-5`, `anthropic/claude-opus-5.5`, `deepseek/deepseek-v4-pro`… (or `gateway:<id>`) | `AI_GATEWAY_API_KEY` or `VERCEL_OIDC_TOKEN`, optional `AI_GATEWAY_BASE_URL` |
| Anthropic | `anthropic:claude-opus-5-5` (or a bare `claude-…` id) | `ANTHROPIC_API_KEY` |
| OpenAI | `openai:gpt-5` | `OPENAI_API_KEY`, optional `OPENAI_BASE_URL` (Azure, proxies) |
| Google Gemini | `google:gemini-2.5-pro` | `GEMINI_API_KEY` |
| OpenRouter | `openrouter:meta-llama/llama-3.3-70b-instruct` | `OPENROUTER_API_KEY` |
| DeepSeek | `deepseek:deepseek-v4-pro`, `deepseek:deepseek-flash` | `DEEPSEEK_API_KEY`, optional `DEEPSEEK_BASE_URL` |
| Ollama | `ollama:qwen3:32b` | `OLLAMA_BASE_URL` (default `http://localhost:11434/v1`) |
| Any OpenAI-compatible API | `<name>:<model>`, e.g. `groq:llama-3.3-70b-versatile` | `OPENSRE_PROVIDER_<NAME>_BASE_URL`, `OPENSRE_PROVIDER_<NAME>_API_KEY` |

The model has to support tool calling.

<details>
<summary>Provider details</summary>

Anthropic models use prompt caching, effort and server-side refusal fallbacks; through the gateway they use its Anthropic Messages API, so caching, effort and thinking blocks are kept (fallbacks are the gateway's own). Other providers go through the OpenAI Chat Completions format, where effort becomes `reasoning_effort` (only sent when set; DeepSeek accepts up to `max`). Reasoning that a provider returns (DeepSeek's `reasoning_content`, the gateway's `reasoning_details`) is sent back to that provider on later turns, as its tool loop requires. Switching models between phases resets the prompt cache once, at the switch.

</details>

## Configuration

All settings are environment variables; [`.env.example`](.env.example) has them all with comments.

| Variable | Default | |
|---|---|---|
| `OPENSRE_DB_URL` | (required) | Postgres URL |
| `OPENSRE_SECRET_KEY` | (required) | encrypts connector credentials (AES-256-GCM) |
| `OPENSRE_API_TOKEN` | empty = no auth | bearer token for the API |
| `OPENSRE_WEBHOOK_TOKEN` | API token | token for `/webhooks/*` |
| `OPENSRE_MODEL` | `anthropic:claude-opus-5-5` | default model for every phase, as `provider:model` ([Models](#models)) |
| `OPENSRE_EFFORT` | provider default | `low`, `medium`, `high`, `xhigh`, `max` |
| `OPENSRE_TRIAGE_MODEL` / `OPENSRE_TRIAGE_EFFORT` | `OPENSRE_MODEL` / `OPENSRE_EFFORT` | model until the mitigation is published |
| `OPENSRE_INVESTIGATE_MODEL` / `OPENSRE_INVESTIGATE_EFFORT` | `OPENSRE_MODEL` / `OPENSRE_EFFORT` | model for the root cause and report |
| `AI_GATEWAY_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, `OPENROUTER_API_KEY`, `DEEPSEEK_API_KEY`, `OLLAMA_BASE_URL` | | credentials of the providers you use |
| `OPENSRE_MAX_TOOL_CALLS` | `30` | per investigation |
| `OPENSRE_MITIGATION_TOOL_CALLS` | `6` | tool calls before the agent must propose a mitigation |
| `OPENSRE_INVESTIGATION_TIMEOUT` | `8m` | |
| `OPENSRE_DEDUPE_WINDOW` | `30m` | |
| `OPENSRE_ACTIONS_ENABLED` | `false` | let investigations propose machine actions a human can approve (requires `OPENSRE_API_TOKEN`) |
| `OPENSRE_ACTION_TTL` | `30m` | how long a proposed action can be approved |
| `OPENSRE_HEALTHCHECK_INTERVAL` | empty = off | check Fly machine metrics on this schedule, e.g. `1h` (at least `5m`), and investigate breaches ([Health checks](#health-checks)) |
| `SLACK_BOT_TOKEN`, `SLACK_CHANNEL` | | bot needs `chat:write` |
| `SLACK_SIGNING_SECRET` | | enables Apply/Reject buttons ([Approve from Slack](#approve-from-slack)) |
| `SLACK_APPROVERS` | empty = anyone in the channel | Slack user IDs allowed to click Apply/Reject |
| `OPENSRE_MCP_ENABLED` | `false` | serve the [MCP tools](#mcp-server) at `/mcp` (requires `OPENSRE_API_TOKEN`) |
| `OPENSRE_MCP_TOKEN` | API token | the only token `/mcp` accepts when set; it reaches the MCP tools, not the whole API |

## CLI tips

Every command has `--help` with examples.

- **Secrets stay off the command line.** For a secret connector key, pass `token` alone to be prompted (input is hidden), `token=@file` to read a file, or `token=-` to read stdin.
- **Ctrl-C** cancels a prompt right away, and stops watching an investigation, but it keeps running on the server. Resume with `opensre show <id> --follow`.
- **Destructive commands ask first:** `connector rm`, `actions approve`, and `catalog import --prune` when it would delete something. Without a terminal, or with `--no-input`, they fail unless you pass `--yes`. `actions approve` and `reject` ask for your name when neither `--as` nor `OPENSRE_USER` is set.
- **Scripting:** data goes to stdout and messages to stderr. `--json` prints JSON (`investigate --json` prints one event per line), `-q` silences messages, and `investigate --no-watch` prints only the investigation ID. Exit codes: `0` success, `1` error, `2` bad usage, `130` interrupted.
- **Completions:** `opensre completion bash|zsh|fish|powershell`.

## API

The API is [ConnectRPC](https://connectrpc.com), defined in [`proto/opensre/v1`](proto/opensre/v1): `CatalogService`, `ConnectorService`, `InvestigationService`, `ActionService`. The same endpoints speak Connect, gRPC and gRPC-Web, plus plain JSON:

```sh
curl -H "Authorization: Bearer $OPENSRE_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"api"}' localhost:8080/opensre.v1.CatalogService/GetService
grpcurl -plaintext -H "Authorization: Bearer $OPENSRE_API_TOKEN" localhost:8080 list   # reflection is on
```

`InvestigationService/WatchInvestigation` streams steps live.

## MCP server

opensre is also an MCP server, so you can use it from Claude Code, Claude Desktop, Cursor or a coding agent: read what opensre sees on a service, the mitigation and report of an investigation, or start one ([ADR 0021](docs/adr/0021-mcp-server.md)).

| Tool | |
|---|---|
| `list_services`, `get_service` | the catalog |
| `get_brief` | the brief of a service, without the LLM (free) |
| `list_investigations`, `get_investigation` | investigations, their mitigation and report; `include_steps` for the audit trail |
| `wait_investigation` | wait until the mitigation is published (or the report written), with progress |
| `start_investigation` | start an investigation: spends LLM tokens and posts to Slack |
| `list_actions`, `get_action` | proposed machine actions and their outcome |

Every tool is read-only except `start_investigation`, which only starts what a webhook or `opensre investigate` would. MCP clients cannot approve machine actions or change the catalog or connectors: approval stays with a named human, in the CLI or Slack.

**Over stdio**, for clients that launch a command. `opensre mcp` calls your server with the API token:

```sh
claude mcp add opensre -e OPENSRE_URL=https://opensre.example.com -e OPENSRE_API_TOKEN=... -- opensre mcp
```

**Over HTTP**, at `/mcp` on the server, with `OPENSRE_MCP_ENABLED=true` (requires `OPENSRE_API_TOKEN`). Clients send `Authorization: Bearer <token>`. Set `OPENSRE_MCP_TOKEN` to give MCP clients a token that reaches only these tools, not the whole API:

```sh
claude mcp add --transport http opensre https://opensre.example.com/mcp --header "Authorization: Bearer $OPENSRE_MCP_TOKEN"
```

Investigations started from MCP have source `mcp`, with the client's name in their alert.

## Development

The dev stack includes a demo app that triggers a fake incident after 3 minutes, so you can watch a full investigation locally:

```sh
docker compose -f docker-compose.dev.yml up -d     # Postgres :55432, Loki :3100, demo app
export OPENSRE_DB_URL="postgres://opensre:opensre@localhost:55432/opensre?sslmode=disable" OPENSRE_SECRET_KEY=dev
go run ./cmd/opensre serve
go run ./cmd/opensre connector add loki loki url=http://localhost:3100
go run ./cmd/opensre catalog import examples/catalog/services.yaml --runbooks examples/catalog/runbooks
go run ./cmd/opensre brief api
```

```sh
go test ./...                # add OPENSRE_TEST_DB_URL=... to run the end-to-end agent test
buf lint && buf generate     # after editing proto/
sqlc generate                # after editing internal/store/queries or migrations
```

## Contributing

Contributions are welcome: read [CONTRIBUTING.md](CONTRIBUTING.md) to get started, and [SECURITY.md](SECURITY.md) to report a vulnerability privately. Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md).

## License

opensre is released under the [MIT license](LICENSE).
