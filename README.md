# opensre

An open-source AI SRE agent you host yourself. It knows your services (owners, dependencies, logs, deploys, runbooks). When an alert fires, it gathers the context, **proposes a mitigation first, then investigates the root cause** with Claude using read-only tools, and posts both to Slack. It **suggests, it never executes**.

See [PLAN.md](PLAN.md) for the design and roadmap.

## How it works

1. **Trigger:** an openstatus, Alertmanager, Grafana or generic webhook arrives, or someone runs `opensre investigate <service>`. The alert is matched to a catalog service (openstatus monitor id, `service`/`app` labels). Repeated alerts within `OPENSRE_DEDUPE_WINDOW` are deduplicated.
2. **Orient (no LLM):** opensre builds a brief with:
   - the service card and its dependencies and blast radius
   - log error patterns, compared with the preceding window of the same length
   - recent deploys or commits of the service and its dependencies
   - its runbooks
   - similar past investigations
3. **Mitigate first:** what matters most is how fast user impact stops, not how fast the bug is understood (as in [Google's SRE practice](https://cloud.google.com/blog/topics/developers-practitioners/how-google-sres-use-gemini-cli-to-solve-real-world-outages)). From the brief and a few tool calls, Claude picks one generic mitigation from a closed set: `rollback`, `restart`, `scale_up`, `drain`, `disable_feature`, `block_traffic`, `failover`, `degrade` or `none`. It fills in the target and exact command, how to verify it worked, how to undo it, and the risk (`safe`, `reversible`, `destructive`). The mitigation is published right away to the API, Slack (with a thread reply so on-call gets notified) and the audit trail, and its time is stored as `mitigated_at`. After `OPENSRE_MITIGATION_TOOL_CALLS` tool calls the agent is told to propose one. A report submitted before a mitigation is rejected.
4. **Investigate:** with a mitigation in hand, Claude looks for the root cause and tests hypotheses with read-only tools:
   - `log_patterns`, `query_logs`, `log_timeline`
   - `recent_deploys`, `get_commit`
   - `get_service`, `list_services`
   - `read_runbook`, `search_past_investigations`

   Tool output is aggregated, size-capped and redacted (tokens, passwords, emails) before it reaches the model. If the evidence shows the mitigation will not help (say, a restart cannot fix a bad config push), Claude revises it.
5. **Report:** a structured report: likely cause, confidence, evidence, ruled out, the mitigation given the root cause, the long-term fix, next steps. It goes to Slack and the API. Every brief, tool call and model turn is stored as an audit trail.

Logs are queried on demand. opensre never ingests or stores your logs, only the aggregated evidence each investigation used.

## Run it

```sh
cp .env.example .env   # set OPENSRE_SECRET_KEY, OPENSRE_API_TOKEN, ANTHROPIC_API_KEY, Slack (optional)
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
- **Destructive actions ask first:** `connector rm`, and `catalog import --prune` when it would delete something. Without a terminal, or with `--no-input`, they fail unless you pass `--yes`.
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
  runbooks: [runbooks/api-5xx.md]
  monitors: ["openstatus:1234"]        # alert → service mapping
```

### Log connectors

| Kind | Add it | `logs.selector` | `catalog discover` proposes |
|---|---|---|---|
| `loki` | `connector add loki loki url=http://loki:3100` (+ `tenant_id`, `username`, `password`, `bearer_token`) | LogQL stream selector: `{service_name="api"}` | one service per `service_name` / `service` / `app` / `job` / `container` label value |
| `axiom` | `connector add axiom axiom token=xaat-...` (+ `url` for other regions, `org_id` for personal tokens, `message_field`) | Start of an APL query: `['logs']` or `['logs'] \| where ['service.name'] == "api"` | one service per `service.name` / `service` / `app` value in each log dataset, or one per dataset |
| `cloudwatch` | `connector add cw cloudwatch region=eu-west-1` (+ `access_key_id`, `secret_access_key`, `session_token`, `endpoint`) | Log group(s), optionally followed by Insights commands: `/ecs/api` or `/ecs/prod, /ecs/worker \| filter service = "api"` | one service per log group, named after its last path segment |

Error filters are RE2 regexes applied to the log line: Loki's line, Axiom's message field (detected from `message`, `msg`, `body`, `log`, ... or set with `message_field`), CloudWatch's `@message`.

Without `access_key_id`/`secret_access_key`, the CloudWatch connector uses the default AWS credential chain of the opensre server (environment, shared config, IAM role). It needs `logs:DescribeLogGroups`, `logs:StartQuery`, `logs:GetQueryResults` and `logs:StopQuery`.

### Fly.io

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

## API

The API is [ConnectRPC](https://connectrpc.com), defined in [`proto/opensre/v1`](proto/opensre/v1): `CatalogService`, `ConnectorService`, `InvestigationService`. The same endpoints speak Connect, gRPC and gRPC-Web, plus plain JSON:

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
| `ANTHROPIC_API_KEY` | | required for investigations |
| `OPENSRE_MODEL` / `OPENSRE_EFFORT` | `claude-opus-5-5` / `high` | |
| `OPENSRE_MAX_TOOL_CALLS` | `30` | per investigation |
| `OPENSRE_MITIGATION_TOOL_CALLS` | `6` | tool calls before the agent must propose a mitigation |
| `OPENSRE_INVESTIGATION_TIMEOUT` | `8m` | |
| `OPENSRE_DEDUPE_WINDOW` | `30m` | |
| `SLACK_BOT_TOKEN`, `SLACK_CHANNEL` | | bot needs `chat:write` |

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
