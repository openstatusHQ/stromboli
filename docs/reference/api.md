# HTTP and ConnectRPC API reference

stromboli serves everything on one port (`STROMBOLI_ADDR`, default `:8080`), in plain HTTP/1.1 and unencrypted HTTP/2. TLS is expected to be terminated in front of it.

## HTTP routes

| Route | Auth | |
|---|---|---|
| `/stromboli.v1.<Service>/<Method>` | API token | the [ConnectRPC API](#connectrpc-services) |
| `/grpc.health.v1.Health/*` | none | gRPC health checks for the four services |
| `/grpc.reflection.v1.ServerReflection/*`, `/grpc.reflection.v1alpha.ServerReflection/*` | none | gRPC server reflection |
| `POST /webhooks/{openstatus,alertmanager,grafana,generic}` | webhook token | [alert webhooks](webhooks.md) |
| `POST /slack/interactions` | Slack request signature | Apply/Reject buttons; only with `SLACK_SIGNING_SECRET`, Slack and actions enabled |
| `/mcp` | MCP token or API token | [MCP over HTTP](mcp-tools.md); only with `STROMBOLI_MCP_ENABLED=true` |
| `GET /healthz` | none | `200 ok` when Postgres answers a ping, else `503 database unavailable` |

## Authentication

Every RPC needs `Authorization: Bearer <STROMBOLI_API_TOKEN>`. A wrong or missing token returns `unauthenticated` (`invalid or missing API token`). When `STROMBOLI_API_TOKEN` is empty, the API is open and the server logs a warning on startup.

## Protocols

The services are defined in [`proto/stromboli/v1`](../../proto/stromboli/v1) and served with [ConnectRPC](https://connectrpc.com). Each endpoint speaks the Connect protocol (including plain JSON over HTTP POST), gRPC and gRPC-Web:

```sh
curl -H "Authorization: Bearer $STROMBOLI_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"api"}' localhost:8080/stromboli.v1.CatalogService/GetService

grpcurl -plaintext -H "Authorization: Bearer $STROMBOLI_API_TOKEN" localhost:8080 list
```

RPCs marked *no side effects* can also be called with Connect GET requests. JSON uses the protobuf JSON mapping: field names in `lowerCamelCase` (proto names are accepted on input), `int64` as strings, timestamps in RFC 3339.

The API stays backwards compatible across releases (`buf breaking` runs on every change).

## ConnectRPC services

### CatalogService

| RPC | | Errors |
|---|---|---|
| `ListServices` | every service, sorted by name. *No side effects* | |
| `GetService(name)` | the service, plus its `upstream` and `downstream` service names. *No side effects* | `not_found` |
| `UpsertService(service)` | create or replace one service; returns validation `issues` (warnings) | `invalid_argument` when the resulting catalog has errors |
| `DeleteService(name)` | | `not_found` |
| `ImportCatalog(services_yaml, runbooks, prune, dry_run)` | upsert services and runbooks from YAML; with `prune`, delete those not in the request. Returns counts and `issues` | `invalid_argument` when the catalog has errors |
| `ExportCatalog` | `services_yaml` and every runbook. *No side effects* | |
| `ValidateCatalog(check_connectors)` | `issues` of the stored catalog; with `check_connectors`, a test query per selector. *No side effects* | |
| `DiscoverServices(connector)` | proposed `candidates` from a log or metrics connector. *No side effects* | `failed_precondition` for an unknown or unsupported connector |

### ConnectorService

| RPC | | Errors |
|---|---|---|
| `CreateConnector(name, kind, config, replace)` | validate keys, encrypt secrets, store. Returns the connector without secret values | `invalid_argument` (unknown kind or key, missing required key), `already_exists` without `replace` |
| `ListConnectors` | every connector; secret keys are listed with an empty value. *No side effects* | |
| `DeleteConnector(name)` | | `not_found` |
| `TestConnector(name)` | `ok` and a `message`: a failed check is `ok: false`, not an RPC error. *No side effects* | |

### InvestigationService

| RPC | | Errors |
|---|---|---|
| `StartInvestigation(service, title, description)` | start an investigation (source `manual`, title default `Manual investigation of <service>`) and return it right away, status `running` | `invalid_argument` without service; `failed_precondition` when no LLM is configured or the service is not in the catalog |
| `GetInvestigation(id)` | the investigation and all its `steps`. *No side effects* | `invalid_argument` (bad UUID), `not_found` |
| `ListInvestigations(limit)` | newest first; `limit` defaults to 50, at most 200. *No side effects* | |
| `WatchInvestigation(id, after_seq)` | server stream: every step with `seq > after_seq` as it is recorded, then one `finished` event with the investigation, then the stream ends. Polls every second | `not_found` |
| `GetBrief(service, since)` | the Markdown brief, built without the LLM. `since` defaults to `1h`, at most `168h`. *No side effects* | `invalid_argument` (bad duration), `failed_precondition` (unknown service) |

`StartInvestigation` called by the MCP server is recorded with source `mcp` and the client's name in the alert (`mcp_client`).

### ActionService

| RPC | | Errors |
|---|---|---|
| `ListActions(investigation_id, status, limit)` | newest first; `limit` defaults to 50, at most 200. *No side effects* | `invalid_argument` (bad UUID) |
| `GetAction(id)` | *No side effects* | `not_found` |
| `ApproveAction(id, approver, note)` | re-validate, claim and run the action synchronously; returns it `succeeded` or `failed` | `not_found`; `failed_precondition` when it is not `proposed`, has expired, no `approver` was given, or can no longer run |
| `RejectAction(id, approver, reason)` | | `not_found`; `failed_precondition` when it is not `proposed` |

## Messages

### Investigation

| Field | Type | |
|---|---|---|
| `id` | string | UUID |
| `title` | string | |
| `source` | string | `manual`, `mcp`, `openstatus`, `alertmanager` (also for Grafana), `webhook`, `healthcheck` |
| `services` | string[] | |
| `status` | enum | `INVESTIGATION_STATUS_RUNNING`, `_COMPLETED`, `_FAILED` |
| `alert` | object | the alert payload, plus `description` and `observed_at` |
| `mitigation` | [Mitigation](#mitigation) | set as soon as triage proposes it |
| `mitigated_at` | timestamp | when the first mitigation was proposed |
| `report` | [Report](#report) | set when completed |
| `error` | string | set when failed |
| `created_at`, `finished_at` | timestamp | |
| `input_tokens`, `output_tokens` | int64 | totals over every model turn (input includes cache reads and writes) |

### Mitigation

| Field | |
|---|---|
| `kind` | `rollback`, `restart`, `scale_up`, `drain`, `disable_feature`, `block_traffic`, `failover`, `degrade` or `none` |
| `target` | what it applies to: service, deploy SHA, region, instance, flag, dependency |
| `action` | the exact command(s) a human runs |
| `rationale` | why it fits the symptoms |
| `verify` | how to tell it worked |
| `undo` | how to revert it |
| `risk` | `safe`, `reversible` or `destructive` |
| `confidence` | `low`, `medium` or `high` |
| `impact` | current user impact |
| `machine_action` | [MachineAction](#machineaction), when stromboli can run it after approval |
| `action_id` | ID of the [Action](#action) created for `machine_action` |

### Report

| Field | |
|---|---|
| `summary` | what is happening, in one sentence |
| `likely_cause` | the most likely cause, or `unknown` |
| `confidence` | `low`, `medium` or `high` |
| `evidence` | observations backing the cause |
| `ruled_out` | hypotheses checked and rejected, with the reason |
| `suggested_mitigation` | the mitigation in light of the cause: `confirmed`, what to do instead, or `none` |
| `fix` | the long-term fix, or `unknown` |
| `next_steps` | what a human should check next |

### Step

One entry of an investigation's audit trail.

| Field | |
|---|---|
| `seq` | position in the investigation, increasing |
| `kind` | `brief`, `tool_call`, `llm`, `mitigation`, `report` or `error` |
| `name` | the service (`brief`), tool (`tool_call`), model (`llm`) or mitigation kind (`mitigation`) |
| `input` | the tool input (`tool_call`); turn, task, model, stop reason and token counts (`llm`) |
| `output` | the brief, tool result, model text, mitigation or report JSON, or error |
| `is_error` | the step failed |
| `duration_ms`, `created_at` | |

### MachineAction

| Field | |
|---|---|
| `service` | catalog service |
| `action` | `restart`, `clone`, `cordon`, `uncordon`, `stop` or `start` |
| `machine_ids` | machines to act on |
| `region` | `clone` only: region of the new machines; default the source's |
| `app` | Fly app, resolved from the catalog |

### Action

| Field | |
|---|---|
| `id`, `investigation_id`, `service` | |
| `machine_action` | [MachineAction](#machineaction) |
| `connector` | the `fly_machines` connector that runs it |
| `status` | `ACTION_STATUS_PROPOSED`, `_RUNNING`, `_SUCCEEDED`, `_FAILED`, `_REJECTED`, `_EXPIRED`, `_SUPERSEDED` |
| `decided_by`, `note` | who approved or rejected it, and their note |
| `result` | what was done machine by machine, and how to undo it |
| `error` | |
| `created_at`, `expires_at`, `decided_at`, `finished_at` | |

### Service, Runbook, ValidationIssue, Connector

See the [catalog reference](catalog.md) and [connectors reference](connectors.md). A `ValidationIssue` has `service`, `field`, `message` and `warning` (`false` = error).
