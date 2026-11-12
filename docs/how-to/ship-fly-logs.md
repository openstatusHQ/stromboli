# How to ship Fly.io logs to a log store

Fly.io has no API to query past logs. Logs of the whole organisation are published on a NATS stream, and Fly's [log shipper](https://github.com/superfly/fly-log-shipper) forwards them to a log store. Ship them to Loki, Axiom or CloudWatch, then connect that store to stromboli.

## 1. Deploy the log shipper

```sh
fly launch --image flyio/log-shipper:latest --no-public-ips --name log-shipper   # set internal_port = 8686
fly secrets set -a log-shipper ORG=<org-slug> ACCESS_TOKEN="$(fly tokens create readonly <org-slug>)"
```

## 2. Point it at your store

Pick one:

```sh
# Loki
fly secrets set -a log-shipper LOKI_URL=https://loki.example.com LOKI_USERNAME=... LOKI_PASSWORD=...
# Axiom
fly secrets set -a log-shipper AXIOM_TOKEN=xaat-... AXIOM_DATASET=fly-logs
# CloudWatch
fly secrets set -a log-shipper AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_REGION=eu-west-1 CLOUDWATCH_LOG_GROUP_NAME=/fly/logs
```

## 3. Connect the store and discover one service per app

| Store | Connector | Selector `catalog discover` proposes |
|---|---|---|
| Loki | `stromboli connector add loki loki url=...` | `{fly_app_name="checker"}` |
| Axiom | `stromboli connector add axiom axiom` (logs in in a browser) | `['fly-logs'] \| where ['fly.app.name'] == "checker"` |
| CloudWatch | `stromboli connector add cw cloudwatch region=eu-west-1 log_group_prefix=/fly/ service_field=fly.app.name` | `/fly/logs \| filter fly.app.name = "checker"` |

The CloudWatch shipper writes every app to one log group: `service_field` makes discovery split it per app with one Insights query, and `log_group_prefix` keeps that query to the Fly group.

```sh
stromboli catalog discover loki > fly-logs.yaml
stromboli catalog import fly-logs.yaml
```

The shipper writes JSON events (`message`, `log.level`, `fly.app.name`, `fly.region`, `host`). stromboli shows the `message` with its level, so error patterns are not split by region or instance.

## See also

- [Connect metrics: Fly.io](connect-metrics.md#flyio)
- [Connect a log store](connect-logs.md)
