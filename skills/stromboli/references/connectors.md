# Connector kinds and catalog selectors

Read this when adding a connector or writing a catalog entry. **Bold** keys are required, 🔒 keys are secret: pass them as `key=@file` or `key=-` (stdin), never inline.

| Kind | Role | Keys |
|---|---|---|
| `loki` | logs | **`url`**, `tenant_id`, `username`, `password` 🔒, `bearer_token` 🔒 |
| `axiom` | logs (OAuth in a browser: the user runs it) | `org_id`, `message_field`, `mcp_url` (`https://mcp.eu.axiom.co/mcp` for EU) |
| `cloudwatch` | logs | `region`, `access_key_id`, `secret_access_key` 🔒, `session_token` 🔒, `log_group_prefix`, `service_field` |
| `gcp_logging` | logs | **`project`**, `credentials_json` 🔒 (`credentials_json=@key.json`; else ADC) |
| `prometheus` | metrics (Prometheus, Mimir, Thanos, VictoriaMetrics, Grafana Cloud) | **`url`**, `tenant_id`, `username`, `password` 🔒, `bearer_token` 🔒, `service_label` |
| `fly` | metrics + machine list | **`org`**, **`token`** 🔒. A read-only token is enough: `fly tokens create readonly <org>` |
| `gcp_monitoring` | metrics | **`project`**, `credentials_json` 🔒 |
| `github` | deploys and commits | `token` 🔒 (read-only; without it, public repos only), `base_url` (GHES) |
| `vercel` | logs + deploys (OAuth in a browser: the user runs it) | **`team_id`** |
| `fly_machines` | machine actions (can act on machines) | **`org`**, **`token`** 🔒 (`fly tokens create deploy -a <app>`). Keep it separate from `fly` |

Examples:

```sh
stromboli connector add loki loki url=https://loki.example.com username=123 password=@/run/secrets/loki
stromboli connector add fly fly org=personal token=@fly-readonly.token
stromboli connector add github github token=@gh.token
stromboli connector add prod-logs cloudwatch region=eu-west-1 log_group_prefix=/ecs/
```

## Catalog selectors by kind

```yaml
- name: api
  owners: ["@backend"]
  tier: 1
  depends_on: [postgres]
  logs:
    source: loki                       # connector name
    selector: '{service_name="api"}'
  metrics:
    source: fly
    selectors: ['app="api"']
  deploys:
    source: github
    repo: acme/api
    workflow: deploy.yml               # or environment: production, or branch: main
  runbooks: [runbooks/api-5xx.md]
  monitors: ["openstatus:1234"]        # matches openstatus webhook alerts
```

| Kind | `logs.selector` / `metrics.selectors` |
|---|---|
| `loki` | LogQL stream selector: `{service_name="api"}` |
| `axiom` | start of an APL query: `['logs'] \| where ['service.name'] == "api"` |
| `cloudwatch` | log group(s), optional Insights commands: `/ecs/api` or `/ecs/prod \| filter service = "api"` |
| `gcp_logging` | Logging filter: `resource.type="cloud_run_revision" AND resource.labels.service_name="api"` |
| `vercel` | project name or ID, optional environment: `web environment=production` |
| `prometheus` | label matchers: `job="api"` |
| `fly` | `app="checker"` |
| `gcp_monitoring` | `service_name="…"`, `subscription_id="…"`, `queue_id="…"` |

Alertmanager and Grafana alerts match a service through a `service`, `service_name`, `app`, `job` or `container` label equal to its catalog name.
