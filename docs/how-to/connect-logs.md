# How to connect a log store

stromboli reads logs where they already are. Add one connector per log store, then point each catalog service at it with `logs.source` (the connector name) and `logs.selector` (a query in the store's own language).

Every `connector add` tests the connection right away. For secrets, pass the key without a value to be prompted (`token`), read it from a file (`token=@file`) or from stdin (`token=-`).

## Loki

```sh
stromboli connector add loki loki url=http://loki:3100
```

Add what your Loki needs:

| Setup | Extra keys |
|---|---|
| multi-tenant (`X-Scope-OrgID`) | `tenant_id=<tenant>` |
| basic auth (Grafana Cloud: user ID and an access policy token) | `username=<id> password` |
| bearer token | `bearer_token` |

Selector: a LogQL stream selector, for example `{service_name="api"}`.

## Axiom

```sh
stromboli connector add axiom axiom token
```

| Setup | Extra keys |
|---|---|
| a personal token (instead of an API token) | `org_id=<org>` |
| the message is in an unusual field | `message_field=<field>` (detected from `message`, `msg`, `body`, `log`… otherwise) |

Datasets in any region (US, EU…) work as is: queries go to the region that stores each dataset.

Selector: the start of an APL query, for example `['logs']` or `['logs'] | where ['service.name'] == "api"`.

## AWS CloudWatch Logs

```sh
stromboli connector add cw cloudwatch region=eu-west-1
```

Without keys, the connector uses the server's default AWS credential chain (environment, shared config, IAM role). To use static keys instead:

```sh
stromboli connector add cw cloudwatch region=eu-west-1 access_key_id=AKIA... secret_access_key
```

The principal needs `logs:DescribeLogGroups`, `logs:StartQuery`, `logs:GetQueryResults` and `logs:StopQuery`. Use `endpoint=` for LocalStack or a VPC endpoint, `session_token` for temporary credentials.

Selector: one or more log groups, optionally followed by Logs Insights commands, for example `/ecs/api` or `/ecs/prod, /ecs/worker | filter service = "api"`.

If many services share one log group, set `service_field` (and `log_group_prefix` to limit discovery to some groups) so `catalog discover` splits the group per value of that field. See [Ship Fly.io logs](ship-fly-logs.md) for an example.

## Google Cloud Logging

```sh
stromboli connector add gcp-logs gcp_logging project=<project-id>
```

Without `credentials_json`, Application Default Credentials are used (`GOOGLE_APPLICATION_CREDENTIALS`, workload identity, or `gcloud auth application-default login`). To use a service account key:

```sh
stromboli connector add gcp-logs gcp_logging project=<project-id> credentials_json=@key.json
```

The principal needs `roles/mcp.toolUser` and `roles/logging.viewer`. Calls are billed to `project`.

Selector: a Logging query language filter, for example `resource.type="cloud_run_revision" AND resource.labels.service_name="api"`.

## Point services at it

Let the connector propose entries, then review and import them:

```sh
stromboli catalog discover loki > services.yaml   # one entry per service it sees
$EDITOR services.yaml                              # add owners, depends_on, runbooks
stromboli catalog import services.yaml
stromboli catalog validate --check-connectors      # runs a test query per selector
```

Or set it by hand on an existing entry:

```yaml
- name: api
  logs:
    source: loki
    selector: '{service_name="api"}'
```

## Check it

```sh
stromboli brief api --since 1h
```

The **Logs** section should show error patterns compared with the previous hour. If it says the source failed, run `stromboli connector test <name>`.

## See also

- [Connect Vercel](connect-vercel.md): runtime logs and deploys, logging in with OAuth
- [Connectors reference](../reference/connectors.md): every key, selector and what `discover` proposes
- [Data access: on demand, MCP first](../explanation/data-access.md)
