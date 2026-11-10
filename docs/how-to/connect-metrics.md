# How to connect metrics

With metrics, the brief shows per-instance memory, CPU, request rate, 5xx and latency against a baseline, and the agent can query more with its `get_metrics` tool. Metrics are read through the Prometheus HTTP API (or PromQL on Google Cloud).

Each service picks a metrics connector with `metrics.source` and selects its series with `metrics.selectors`: PromQL label matchers, ANDed together.

## Prometheus, Mimir, Thanos, VictoriaMetrics, Grafana Cloud

Anything that speaks the Prometheus HTTP API works:

```sh
stromboli connector add prom prometheus url=http://prometheus:9090
```

Add `tenant_id` (sent as `X-Scope-OrgID`), `username` and `password`, or `bearer_token` as your server needs.

There are no presets for a generic Prometheus: the agent lists the metric names a service has with `list_metrics` and writes PromQL itself. In a query, `$selector` expands to the service's matchers.

`catalog discover` proposes one service per value of the first label among `service_name`, `service`, `app` and `job` that has values. To force a label, set `service_label=<label>` on the connector.

```yaml
- name: api
  metrics:
    source: prom
    selectors: ['job="api"']
```

## Fly.io

Fly collects every machine's metrics in a managed Prometheus, so there is nothing to deploy. A **read-only** org token is enough; it cannot deploy, restart or read secrets:

```sh
stromboli connector add fly fly org=<org-slug> token="$(fly tokens create readonly <org-slug>)"
```

`<org-slug>` is in `fly orgs list` (`personal` for a personal org). Revoke the token later with `fly tokens list` and `fly tokens revoke <id>`.

Then let it propose one service per Fly app and import the result:

```sh
stromboli catalog discover fly > fly.yaml
stromboli catalog import fly.yaml
```

Services already in the catalog without metrics come back as their full entry with `metrics` filled in, so the import only adds the metrics.

The `fly` connector has [presets](../reference/metric-presets.md#fly) (memory, OOM kills, CPU, throttling, disk, volumes, HTTP). With a Fly service, also add `runtime: fly.io` (or a `fly:` block) to its entry so the brief lists its machines and the exact Fly commands for each mitigation:

```yaml
- name: checker
  runtime: fly.io
  metrics:
    source: fly
    selectors: ['app="checker"']
```

## Google Cloud Monitoring

```sh
stromboli connector add gcp-metrics gcp_monitoring project=<project-id>
```

Credentials work as for [Cloud Logging](connect-logs.md#google-cloud-logging); the principal needs `roles/mcp.toolUser` and `roles/monitoring.viewer`.

The presets depend on the resource label in the selectors:

| Selector label | Product | Example |
|---|---|---|
| `subscription_id` | Pub/Sub | `subscription_id="checker-ams"` |
| `queue_id` | Cloud Tasks | `queue_id="alerting"` |
| `service_name` | Cloud Run | `service_name="api"` |

`catalog discover gcp-metrics` proposes one service per Pub/Sub subscription, Cloud Tasks queue and Cloud Run service with data in the last hour.

## Check it

```sh
stromboli catalog validate --check-connectors
stromboli brief api
```

The **Metrics** section of the brief should list series with their current value and baseline.

## See also

- [Metric presets](../reference/metric-presets.md)
- [Enable health checks](enable-health-checks.md), which use the Fly metrics
- [ADR 0012: metrics through the Prometheus HTTP API](../adr/0012-metrics-through-prometheus-api.md)
