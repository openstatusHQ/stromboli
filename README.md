# opensre

An open-source AI SRE agent you host yourself. It knows your services (owners, dependencies, logs, metrics, deploys, runbooks). When an alert fires, it gathers the context, **proposes a mitigation first, then investigates the root cause** with the LLM of your choice (Claude by default) using read-only tools, and posts both to Slack. It **suggests; it never acts on its own**. The one exception is opt-in: for services on Fly.io, opensre can run a machine action (restart, clone, cordon…) after a named human approves it ([Machine actions](#machine-actions)).

See [PLAN.md](PLAN.md) for the design and roadmap.

## How it works

1. **Trigger:** an openstatus, Alertmanager, Grafana or generic webhook arrives, or someone runs `opensre investigate <service>`. The alert is matched to a catalog service (openstatus monitor id, `service`/`app` labels). Repeated alerts within `OPENSRE_DEDUPE_WINDOW` are deduplicated.
2. **Orient (no LLM):** opensre builds a brief with:
   - the service card and its dependencies and blast radius
   - log error patterns, compared with the preceding window of the same length
   - key machine and HTTP metrics per instance (memory, CPU, CPU throttling, OOM kills, request rate, 5xx, p95 latency), compared with the same baseline window
   - recent deploys or commits of the service and its dependencies
   - its runbooks
   - similar past investigations
   - for services on Fly.io: the app's machines (ID, region, state, process group, size, image) and the Fly command, with its undo, for each generic mitigation
3. **Mitigate first:** what matters most is how fast user impact stops, not how fast the bug is understood (as in [Google's SRE practice](https://cloud.google.com/blog/topics/developers-practitioners/how-google-sres-use-gemini-cli-to-solve-real-world-outages)). From the brief and a few tool calls, Claude picks one generic mitigation from a closed set: `rollback`, `restart`, `scale_up`, `drain`, `disable_feature`, `block_traffic`, `failover`, `degrade` or `none`. It fills in the target and exact command, how to verify it worked, how to undo it, and the risk (`safe`, `reversible`, `destructive`). The mitigation is published right away to the API, Slack (with a thread reply so on-call gets notified) and the audit trail, and its time is stored as `mitigated_at`. After `OPENSRE_MITIGATION_TOOL_CALLS` tool calls the agent is told to propose one. A report submitted before a mitigation is rejected.
4. **Investigate:** with a mitigation in hand, Claude looks for the root cause and tests hypotheses with read-only tools:
   - `log_patterns`, `query_logs`, `log_timeline`
   - `get_metrics` (a preset such as `memory_used_percent`, or any PromQL), `list_metrics`
   - `recent_deploys`, `get_commit`
   - `get_service`, `list_services`
   - `read_runbook`, `search_past_investigations`

   Tool output is aggregated, size-capped and redacted (tokens, passwords, emails) before it reaches the model. If the evidence shows the mitigation will not help (say, a restart cannot fix a bad config push), Claude revises it.
5. **Report:** a structured report: likely cause, confidence, evidence, ruled out, the mitigation given the root cause, the long-term fix, next steps. It goes to Slack and the API. Every brief, tool call and model turn is stored as an audit trail.

Logs and metrics are queried on demand. opensre never ingests or stores them, only the aggregated evidence each investigation used.

## Run it

```sh
cp .env.example .env   # set OPENSRE_SECRET_KEY, OPENSRE_API_TOKEN, an LLM key (ANTHROPIC_API_KEY...), Slack (optional)
docker compose up -d   # opensre + Postgres on :8080
```

Already have Postgres? Run the image alone with `OPENSRE_DB_URL=postgres://...`. Migrations run on startup.

Then configure it with the CLI (the same binary; `docker compose exec opensre opensre ...` works too):

```sh
export OPENSRE_URL=http://localhost:8080 OPENSRE_API_TOKEN=...

opensre connector add loki loki url=http://loki:3100            # + tenant_id, username, password, bearer_token
opensre connector add github github token                        # for deploys and commits; prompts for the token
opensre catalog discover loki > services.yaml                    # proposes one service per log stream/dataset/group (see below)
opensre catalog import services.yaml --runbooks runbooks/        # add --prune to make the file authoritative
opensre brief api --since 1h                                     # the context brief, no LLM involved
opensre investigate api --description "5xx on /v1/monitors"      # streams the investigation live
```

Every command has `--help` with examples. Some conventions:

- **Secrets stay off the command line.** For a secret connector key, pass `token` alone to be prompted (input is hidden), `token=@file` to read a file, or `token=-` to read stdin.
- **Scripting:** data goes to stdout and messages to stderr. `--json` prints JSON (`investigate --json` prints one event per line), `-q` silences messages, and `investigate --no-watch` prints only the investigation ID. Exit codes: `0` success, `1` error, `2` bad usage, `130` interrupted.
- **Destructive actions ask first:** `connector rm`, `actions approve`, and `catalog import --prune` when it would delete something. Without a terminal, or with `--no-input`, they fail unless you pass `--yes`.
- **Ctrl-C** stops watching an investigation, but the investigation keeps running on the server. Resume with `opensre show <id> --follow`.
- `opensre completion bash|zsh|fish|powershell` prints shell completions.

Point your alerts at it:

| Source | URL |
|---|---|
| openstatus (webhook notification, add header `Authorization: Bearer <token>`) | `POST /webhooks/openstatus` |
| Alertmanager / Grafana alerting | `POST /webhooks/alertmanager?token=<token>` or `/webhooks/grafana` |
| Anything else: `{"service","title","fingerprint","details"}` | `POST /webhooks/generic` |

## The service catalog

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
| `fly` | `connector add fly fly org=<org-slug> token="$(fly tokens create readonly <org-slug>)"` | `app="checker"` | yes, see below |
| `prometheus` | `connector add prom prometheus url=http://prometheus:9090` (+ `tenant_id`, `username`, `password`, `bearer_token`, `service_label`) | `job="api"`, `service="api"` | none: the agent uses `list_metrics` and writes PromQL |
| `gcp_monitoring` | `connector add gcp-metrics gcp_monitoring project=<project-id>` (+ `credentials_json=@key.json`) | `subscription_id="checker-ams"`, `queue_id="alerting"`, `service_name="api"` | yes, see below |

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

### Not on MCP

- **Fly.io** offers MCP only through `flyctl mcp server`, a local process that runs flyctl commands and serves all of them (destroy, exec, secrets) without marking any read-only. opensre calls the Machines API directly instead.
- **CloudWatch:** AWS's MCP server (`awslabs.cloudwatch-mcp-server`) is a local Python process, so opensre uses the AWS SDK.
- **Loki and Prometheus** have no hosted MCP server. Grafana's `mcp-grafana` needs a Grafana instance in front of them.
- **Axiom** hosts an MCP server (`mcp.axiom.co`). Its tool list needs credentials even to read, so its tools have not been checked yet. Until then, opensre uses its REST API.

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

The brief shows memory, CPU, throttling, OOM kills, request rate, 5xx and p95 latency; the agent can query the rest with `get_metrics`.

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

The catalog lives in opensre's database. YAML is its import/export format, so you can keep it in git and sync it from CI with `opensre catalog import services.yaml --runbooks runbooks/ --prune --yes`.

#### Mitigation commands

For a service with `runtime: fly.io` or a `fly:` block, the brief gets a **Fly.io** section. It lists the app's machines from the [Machines API](https://fly.io/docs/machines/api/), so the agent can name real machine IDs and regions (the `instance` label of the Fly metrics is the machine ID). It also gives the Fly command and its undo for each generic mitigation:

| Mitigation | Command | Undo |
|---|---|---|
| restart one machine | `fly machine restart <id> -a <app>` | none needed |
| restart every machine | `fly apps restart <app>` | none needed |
| scale_up, one more machine | `fly machine clone <id> -a <app> --region <region>` | `fly machine destroy <new-id> -a <app> --force` |
| scale_up, more machines / memory | `fly scale count <n>` / `fly scale memory <MB>` | the previous value |
| drain | `fly machine cordon <id>` (no traffic, keeps running) or `fly machine stop <id>` | `uncordon` / `start` |
| rollback | `fly releases --image`, then `fly deploy --image <previous image>` | redeploy the bad image |

Machines with a volume are flagged: a clone would start with an empty volume. The app name is `fly.app`, else the `app="..."` metrics selector, else the service name. The machines are listed with the `fly.actions` connector, else the `fly` metrics connector's token.

#### Machine actions

opensre can run some of these commands itself, behind several guardrails:

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

1. When the brief says actions can run for the service, the agent may add a `machine_action` (`restart`, `clone`, `cordon`, `uncordon`, `stop` or `start`, plus machine IDs and, for `clone`, a region) to its mitigation. opensre checks it against the allow-list and the app's current machines (at most 10 per action; machines with a volume are never cloned). It then stores it as a **proposed** action. A refused action is dropped, the mitigation stays a suggestion, and the agent is told why. Revising the mitigation supersedes the pending action.
2. Nothing runs until a human approves it, before `OPENSRE_ACTION_TTL` (30m) runs out:

   ```sh
   opensre actions --status proposed
   opensre actions approve <action-id> --as alice --note "ok from the incident call"   # asks to confirm; --yes skips
   opensre actions reject <action-id> --as alice --note "the db is the cause"
   ```

   The approval is checked again against the catalog and the machines, then claimed atomically, so an action runs at most once. It then runs machine by machine. A restart, start or clone waits for the machine to reach `started` (60s) and stops at the first failure, so a rolling restart never takes down more than one machine that did not come back. The result lists what was done and how to undo it (for a clone, the `fly machine destroy` of the new machine).
3. Every action keeps who approved or rejected it, when, their note, the result and the error (`opensre actions show <id>`, `ActionService`), and is posted in the investigation's Slack thread.

The approver name is recorded as given: anyone with the API token can approve.

### Approve from Slack

With `SLACK_SIGNING_SECRET` set (and `OPENSRE_ACTIONS_ENABLED=true`), the Slack message of an investigation shows **Apply** and **Reject** buttons under a proposed action instead of the CLI command. Apply asks to confirm, showing the risk and the undo. The click goes through the same `Approve`/`Reject` as the CLI, with the Slack user recorded as the approver (`@alice (Slack U0123)`). The outcome is posted in the thread and the buttons are replaced by it. Expired or superseded actions lose their buttons on the next redraw, and a click on one is answered with why it cannot run.

1. In your Slack app, turn on **Interactivity & Shortcuts** with Request URL `https://<opensre>/slack/interactions`.
2. Set `SLACK_SIGNING_SECRET` (Basic Information → Signing Secret). Requests without a valid Slack signature, or older than 5 minutes, are refused.
3. Optional but recommended: `SLACK_APPROVERS=U0123,U0456` limits the buttons to those Slack user IDs. Without it, anyone who can see the channel can approve.

Only machine actions have buttons. A text-only mitigation (rollback, failover…) is still applied by a human outside opensre.

## API

The API is [ConnectRPC](https://connectrpc.com), defined in [`proto/opensre/v1`](proto/opensre/v1): `CatalogService`, `ConnectorService`, `InvestigationService`, `ActionService`. The same endpoints speak Connect, gRPC and gRPC-Web, plus plain JSON:

```sh
curl -H "Authorization: Bearer $OPENSRE_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"api"}' localhost:8080/opensre.v1.CatalogService/GetService
grpcurl -plaintext -H "Authorization: Bearer $OPENSRE_API_TOKEN" localhost:8080 list   # reflection is on
```

`InvestigationService/WatchInvestigation` streams steps live.

## Configuration

| Variable | Default | |
|---|---|---|
| `OPENSRE_DB_URL` | (required) | Postgres URL |
| `OPENSRE_SECRET_KEY` | (required) | encrypts connector credentials (AES-256-GCM) |
| `OPENSRE_API_TOKEN` | empty = no auth | bearer token for the API |
| `OPENSRE_WEBHOOK_TOKEN` | API token | token for `/webhooks/*` |
| `OPENSRE_MODEL` | `anthropic:claude-opus-5-5` | default model for every task, as `provider:model` ([Models](#models)) |
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
| `SLACK_BOT_TOKEN`, `SLACK_CHANNEL` | | bot needs `chat:write` |

### Models

opensre is not tied to one LLM vendor. As with the [AI SDK](https://ai-sdk.dev/docs/reference/ai-sdk-core/provider-registry), a model is `provider:model`, and each task can use its own:

```sh
OPENSRE_TRIAGE_MODEL=anthropic:claude-haiku-5-5     # fast: time to mitigation matters most
OPENSRE_TRIAGE_EFFORT=low
OPENSRE_INVESTIGATE_MODEL=openai:gpt-5              # thorough: root cause and report
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

Anthropic models use prompt caching, effort and server-side refusal fallbacks; through the gateway they use its Anthropic Messages API, so caching, effort and thinking blocks are kept (fallbacks are the gateway's own). Other providers go through the OpenAI Chat Completions format, where effort becomes `reasoning_effort` (only sent when set; DeepSeek accepts up to `max`). Reasoning that a provider returns (DeepSeek's `reasoning_content`, the gateway's `reasoning_details`) is sent back to that provider on later turns, as its tool loop requires. The model has to support tool calling. Switching models between tasks resets the prompt cache once, at the switch.

## Development

```sh
docker compose -f docker-compose.dev.yml up -d     # Postgres :55432, Loki :3100, demo app (fake incident after 3m)
export OPENSRE_DB_URL="postgres://opensre:opensre@localhost:55432/opensre?sslmode=disable" OPENSRE_SECRET_KEY=dev
go run ./cmd/opensre serve
go run ./cmd/opensre connector add loki loki url=http://localhost:3100
go run ./cmd/opensre catalog import examples/catalog/services.yaml --runbooks examples/catalog/runbooks
go run ./cmd/opensre brief api

go test ./...                                       # add OPENSRE_TEST_DB_URL=... to run the end-to-end agent test
buf lint && buf generate                            # after editing proto/
sqlc generate                                       # after editing internal/store/queries or migrations
```
