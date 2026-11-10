# How to send alerts to stromboli

Each alert source posts to its own webhook. The alert is matched to a catalog service, and an investigation starts on its own. An alert that matches no service is ignored (and logged).

All webhooks are authenticated with `STROMBOLI_WEBHOOK_TOKEN`, or `STROMBOLI_API_TOKEN` when it is not set. Prefer a separate webhook token, so the token you paste into alerting tools cannot call the API. The token can be sent as `?token=`, `Authorization: Bearer <token>` or `X-Webhook-Secret: <token>`.

## openstatus

1. In the service's catalog entry, list the monitors that alert for it:

   ```yaml
   - name: api
     monitors: ["openstatus:1234"]   # the monitor ID
   ```

2. In openstatus, add a **webhook** notification with URL `https://<stromboli>/webhooks/openstatus` and header `Authorization: Bearer <token>`, and attach it to the monitor.

A `recovered` notification marks the incident resolved: the next failure starts a new investigation.

## Prometheus Alertmanager

Add a receiver:

```yaml
receivers:
  - name: stromboli
    webhook_configs:
      - url: https://stromboli.example.com/webhooks/alertmanager?token=<token>
        send_resolved: true
route:
  routes:
    - receiver: stromboli
      matchers: [severity=~"critical|page"]
      continue: true
```

The alert is matched to the catalog service named by its first label among `service`, `service_name`, `app`, `job` and `container`. Make sure your alerts carry one of them with the catalog name. Keep `send_resolved: true`: a resolved alert closes the incident for dedupe.

## Grafana alerting

Add a **Webhook** contact point with URL `https://<stromboli>/webhooks/grafana?token=<token>` and route your alerts to it. Grafana uses the Alertmanager payload, so labels are matched the same way.

## Anything else

Post JSON to `/webhooks/generic`:

```sh
curl -X POST https://stromboli.example.com/webhooks/generic \
  -H "Authorization: Bearer $STROMBOLI_WEBHOOK_TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "service": "api",
    "title": "p95 latency above 2s",
    "fingerprint": "api-latency",
    "details": {"p95_ms": 2400, "dashboard": "https://grafana.example.com/d/api"}
  }'
```

`service` and `title` are required. Without a `fingerprint`, repeats are not deduplicated. Send `"resolved": true` with the same fingerprint when the problem is over.

## Start one by hand

From the CLI, an MCP client, or the API:

```sh
stromboli investigate api --description "5xx on /v1/monitors since 14:05"
```

## Check it

Every webhook answers `202 Accepted` with what happened to each alert:

```json
{"alerts":[{"title":"...","status":"started","investigation_id":"..."}]}
```

`status` is `started`, `deduplicated`, `resolved`, `ignored: no matching service in the catalog`, or `error: …`. A wrong token answers `401` and the server logs `webhook rejected: invalid or missing token`.

## See also

- [Webhooks reference](../reference/webhooks.md): payloads, fingerprints and statuses
- [The life of an investigation](../explanation/investigation-lifecycle.md#1-match-and-dedupe): how repeats are merged
