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
   - any read-only tool of a connected [MCP server](#mcp-connectors), as `mcp__<connector>__<tool>`

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
opensre connector add github github token=ghp_...                # for deploys and commits
opensre connector add k8s mcp url=https://k8s-mcp.internal/mcp bearer_token=...  # any MCP server, see below
opensre catalog discover loki > services.yaml                    # proposes one service per Loki service_name/app label
opensre catalog import services.yaml --runbooks runbooks/        # add --prune to make the file authoritative
opensre brief api --since 1h                                     # the context brief, no LLM involved
opensre investigate api --description "5xx on /v1/monitors"      # streams the investigation live
```

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

The catalog lives in opensre's database. YAML is its import/export format, so you can keep it in git and sync it from CI with `opensre catalog import --prune`.

## MCP connectors

Most of the live context a mitigation depends on (replica and rollout state, which regions are healthy, feature flags, failover targets, traffic) is reached through MCP servers rather than dedicated Go connectors. Add any server that speaks [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports):

```sh
opensre connector add k8s mcp url=https://k8s-mcp.internal/mcp bearer_token=...
opensre connector add flags mcp url=https://flags.internal/mcp tools=get_flag,list_flags   # explicit allowlist
```

At the start of every investigation opensre connects to each MCP connector and offers its tools to Claude as `mcp__<connector>__<tool>`, for both the mitigation and the root-cause phase. opensre still never executes anything:

- Only tools the server marks `readOnlyHint: true` are offered. Set `tools=a,b` to choose the exact list instead.
- Annotations are declared by the server, so give MCP servers read-only credentials too.
- Output is redacted and size-capped like every other tool. A server that is down is skipped and noted in the audit trail.

`opensre connector test <name>` fails when the server is reachable but exposes no tool.

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
