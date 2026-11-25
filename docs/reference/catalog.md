# Service catalog reference

The catalog is a list of services stored in stromboli's database. Its import and export format is a YAML list whose entries have the shape of the `stromboli.v1.Service` message ([`proto/stromboli/v1/catalog.proto`](../../proto/stromboli/v1/catalog.proto)), with `snake_case` field names. Unknown fields are rejected.

```yaml
- name: api                              # required
  description: Public REST API
  owners: ["@team-backend"]
  repo: acme/api
  path: services/api
  runtime: fly.io
  tier: 1
  depends_on: [db-proxy]
  logs:
    source: loki
    selector: '{service_name="api"}'
  metrics:
    source: fly
    selectors: ['app="api"']
  deploys:
    source: github
    repo: acme/api
    workflow: deploy.yml
  runbooks: [runbooks/api-5xx.md]
  monitors: ["openstatus:1234"]
  slos:
    - name: availability
      target: 99.9
  fly:
    app: acme-api
    actions: fly-deploy
    allow: [restart, cordon, uncordon]
  queues:
    - name: jobs
      source: gcp-metrics
      role: consumer
      subscriptions: ['subscription_id=~"jobs-.*"', 'subscription_id!="jobs-dead-letter"']
      topics: ['topic_id=~"jobs-.*"']
```

## Fields

| Field | Type | |
|---|---|---|
| `name` | string | **required**. Unique; lowercase letters, digits, `.`, `_`, `-`, starting with a letter or digit, at most 63 characters |
| `description` | string | shown in the brief |
| `owners` | list of strings | free-form (`@team-backend`, an email…) |
| `repo` | string | the service's repository, informational |
| `path` | string | path of the service in its repository, informational |
| `runtime` | string | free-form. `fly.io`, `fly` or `flyio` (any case) marks a Fly.io service, as does a `fly` block |
| `tier` | integer | criticality, `1` is the most critical. `0` = unset |
| `depends_on` | list of service names | services this one calls (downstream). Builds the dependency graph |
| `logs` | [LogsConfig](#logs) | where the service's logs are |
| `metrics` | [MetricsConfig](#metrics) | where its metrics are |
| `deploys` | [DeploysConfig](#deploys) | where its deploys or commits are |
| `runbooks` | list of runbook paths | e.g. `runbooks/api-5xx.md`; uploaded with `catalog import --runbooks` |
| `monitors` | list of strings | alert sources mapped to this service. Today `openstatus:<monitor id>` |
| `slos` | list of `{name, target}` | informational |
| `fly` | [FlyConfig](#fly) | Fly.io app and machine actions |
| `queues` | list of [QueueConfig](#queues) | queues the service consumes from or publishes to |

### logs

| Field | |
|---|---|
| `source` | name of a log connector (`loki`, `axiom`, `cloudwatch`, `gcp_logging`, `vercel`) |
| `selector` | the connector's query selecting the service's logs. Syntax per kind: [Connectors](connectors.md#log-connectors) |

### metrics

| Field | |
|---|---|
| `source` | name of a metrics connector (`prometheus`, `fly`, `gcp_monitoring`) |
| `selectors` | PromQL label matchers, ANDed, e.g. `['app="api"']`. Queries reference them as `$selector`. Required when `source` is set |

### deploys

| Field | |
|---|---|
| `source` | name of a `github` or `vercel` connector |
| `repo` | `owner/name`. Required for `github` |
| `workflow` | `github`: workflow file name: deploys are its completed runs |
| `environment` | `github`: deployment environment: deploys are its deployments. `vercel`: deployment target, `production` (default) or `preview` |
| `branch` | `github`: branch for the commit fallback (neither `workflow` nor `environment`). Default: the repository's default branch. `vercel`: only deploys of this branch |
| `project` | `vercel`: project name or ID. Required for `vercel` |

### fly

| Field | |
|---|---|
| `app` | Fly app name. Default: the `app="…"` metrics selector, else the service name |
| `actions` | name of a `fly_machines` connector. With it (and `STROMBOLI_ACTIONS_ENABLED=true`), stromboli can run machine actions after approval |
| `allow` | machine actions stromboli may run: `restart`, `clone`, `cordon`, `uncordon`, `stop`, `start`. Default: `restart`, `clone`, `cordon`, `uncordon` |

### queues

A service's own `metrics` describe where it runs; `queues` add the message queues it works with, so a worker on Fly.io that consumes from Pub/Sub has both in its brief ([ADR 0024](../adr/0024-queues-in-the-catalog.md)). Today a queue is Pub/Sub, read through a `gcp_monitoring` connector.

| Field | |
|---|---|
| `name` | **required**. Unique within the service; same format as service names. `get_metrics` and `list_metrics` take it as `queue` |
| `source` | **required**. Name of a `gcp_monitoring` connector |
| `role` | **required**. `consumer` or `producer` |
| `subscriptions` | PromQL label matchers selecting the subscriptions consumed, ANDed, with a positive `subscription_id` matcher: `['subscription_id=~"jobs-.*"', 'subscription_id!="jobs-dead-letter"']`. Required for a consumer |
| `topics` | PromQL label matchers selecting topics, ANDed, with a positive `topic_id` matcher: the topics a producer publishes to (required), or those feeding a consumer (optional: adds the publish rate) |

The brief gets a **Queues** section: totals against the baseline (published, acknowledged, backlog, ack latency), then for each failure signal how many subscriptions show it in the window and in the baseline, worst first. The signals are nacks, redeliveries (deliveries per ack above 1.1 on average), dead-lettered messages, expired ack deadlines, no consumer connected for the whole window, suspended streams, and a backlog or oldest message above twice its baseline. For a producer: failed publish requests, and topics that stopped publishing.

Queues do not add edges to the dependency graph. Once a service has `queues`, older stromboli versions cannot load the catalog: remove them before downgrading.

## Runbooks

Runbooks are Markdown files stored with the catalog. `catalog import --runbooks <dir>` uploads every `*.md` file under `<dir>` as `runbooks/<relative path>`; `catalog export --runbooks <dir>` writes them back. The agent reads them with `read_runbook`, and the brief lists the service's runbooks.

## Validation

Validation runs on import (`--dry-run` included), on `UpsertService` and with `catalog validate`. **Errors** block a write; **warnings** do not.

| Field | Rule | Level |
|---|---|---|
| `name` | invalid format | error |
| `name` | duplicate | error |
| `depends_on` | the service itself | error |
| `depends_on` | unknown service (add external dependencies too) | warning |
| `depends_on` | part of a dependency cycle | warning |
| `logs` | no source or selector | warning |
| `logs.source` | unknown connector | warning |
| `logs.source` | connector is not a log connector | error |
| `metrics.selectors` | empty while `metrics.source` is set | error |
| `metrics.source` | unknown connector | warning |
| `metrics.source` | connector is not a metrics connector | error |
| `deploys.repo` | empty while `deploys.source` is a `github` connector | error |
| `deploys.project` | empty while `deploys.source` is a `vercel` connector | error |
| `deploys.source` | unknown connector | warning |
| `deploys.source` | connector is not `github` or `vercel` | error |
| `fly.allow` | unknown action | error |
| `fly.actions` | unknown connector | warning |
| `fly.actions` | connector is not `fly_machines` | error |
| `queues[…].name` | missing, invalid or duplicate | error |
| `queues[…].role` | not `consumer` or `producer` | error |
| `queues[…].subscriptions` | empty for a consumer, or no positive `subscription_id` matcher | error |
| `queues[…].subscriptions` | set on a producer (ignored) | warning |
| `queues[…].topics` | empty for a producer, or no positive `topic_id` matcher | error |
| `queues[…].source` | missing, or not a `gcp_monitoring` connector | error |
| `queues[…].source` | unknown connector | warning |
| `runbooks` | runbook not found | warning |
| `owners` | empty | warning |

`catalog validate --check-connectors` also runs a test query for each service's log and metrics selector, and each queue's subscription and topic matchers.

## Dependency graph

`depends_on` defines the graph used in briefs and by the `get_service` tool:

- **downstream**: the services this one depends on;
- **upstream**: the services that depend on this one;
- **blast radius**: every service that transitively depends on this one, which a failure here can reach.
