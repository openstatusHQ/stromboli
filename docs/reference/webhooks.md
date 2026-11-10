# Alert webhooks reference

Alert sources start investigations by posting to `/webhooks/*`. Each request is authenticated, parsed into one or more alerts, matched to catalog services, and either starts an investigation, is merged into one, resolves earlier ones, or is ignored.

## Endpoints

| Method and path | Payload | Investigation source |
|---|---|---|
| `POST /webhooks/openstatus` | [openstatus webhook notification](#openstatus) | `openstatus` |
| `POST /webhooks/alertmanager` | [Alertmanager webhook](#alertmanager-and-grafana) | `alertmanager` |
| `POST /webhooks/grafana` | Grafana alerting webhook (Alertmanager shape) | `alertmanager` |
| `POST /webhooks/generic` | [generic JSON](#generic) | `webhook` |

Bodies are read up to 1 MiB.

## Authentication

The token is `STROMBOLI_WEBHOOK_TOKEN`, or `STROMBOLI_API_TOKEN` when it is unset. With neither set, webhooks are unauthenticated. The request's token is taken from, in increasing priority:

1. the `token` query parameter: `?token=<token>`
2. `Authorization: Bearer <token>` (other schemes are ignored, so a proxy's own `Authorization` doesn't override `?token=`)
3. `X-Webhook-Secret: <token>`

A wrong or missing token answers `401 unauthorized` and logs `webhook rejected: invalid or missing token` with the path and user agent.

## Payloads

### openstatus

The [openstatus webhook notification](https://www.openstatus.dev/docs/reference/notification#webhook). Fields read:

| Field | Use |
|---|---|
| `monitor.id` | **required**. Service: the catalog entry whose `monitors` contains `openstatus:<id>`. Fingerprint: `openstatus:<id>` |
| `monitor.name`, `status`, `errorMessage` | title: `openstatus monitor "<name>" is <status>[: <errorMessage>]` |
| `cronTimestamp` | when the problem was observed (Unix milliseconds) |
| `status` | `recovered` resolves the alert |

The whole payload is kept as the investigation's alert.

### Alertmanager and Grafana

The Alertmanager webhook payload; each element of `alerts` is one alert.

| Field | Use |
|---|---|
| `labels.service`, `labels.service_name`, `labels.app`, `labels.job`, `labels.container` | service: the first of these that names a catalog service |
| `labels.alertname`, `annotations.summary` | title: `<alertname>[: <summary>]` |
| `fingerprint` | fingerprint `alertmanager:<fingerprint>`; without one, `alertmanager:<alertname>/<labels.service>` |
| `startsAt` | when the problem was observed |
| `status` | `resolved` resolves the alert |

The alert keeps `labels`, `annotations`, `startsAt` and `generatorURL`.

### generic

```json
{
  "service": "api",
  "title": "p95 latency above 2s",
  "fingerprint": "api-latency",
  "resolved": false,
  "at": "2026-11-10T14:05:00Z",
  "details": {"any": "json"}
}
```

| Field | |
|---|---|
| `service` | **required**. Catalog service name |
| `title` | **required** |
| `fingerprint` | dedupe key, stored as `generic:<fingerprint>`. Without it, every request starts a new investigation |
| `resolved` | `true` resolves the alert |
| `at` | RFC 3339 time the problem was observed; default now |
| `details` | free-form; kept in the alert as `details` |

## Processing

For each alert, in order:

1. **Resolved** (`recovered`, `resolved`, `"resolved": true`): every unresolved investigation with the same fingerprint is marked resolved. No investigation starts.
2. **No matching service**: ignored, logged as `alert without a catalog service`.
3. **Duplicate**: merged into an existing investigation (see below), logged as `alert deduplicated`.
4. Otherwise a new investigation starts in the background.

### Deduplication

An alert with a fingerprint is merged into the most recent investigation with the same fingerprint that:

- is not resolved, and
- is still running, **or** finished less than `STROMBOLI_DEDUPE_WINDOW` (default `30m`) ago and has no machine action that succeeded.

So a repeat of an alert joins the running investigation; after it finishes, a repeat within the window reuses its result; once the alert resolved, or an approved action fixed something, the alert firing again is a new incident.

### Investigation window

The brief and the agent look at the time from 30 minutes before the alert's time (`at`, `startsAt`, `cronTimestamp`) until now, bounded to the last 24 hours. A time in the future is treated as now.

## Response

`202 Accepted` with one result per alert:

```json
{"alerts": [{"title": "…", "status": "started", "investigation_id": "7c9e6679-…"}]}
```

| `status` | `investigation_id` |
|---|---|
| `started` | the new investigation |
| `deduplicated` | the investigation it was merged into |
| `resolved` | none |
| `ignored: no matching service in the catalog` | none |
| `error: <message>` | none (e.g. no LLM configured) |

Errors before alerts are processed: `400` for an unreadable or invalid payload (with the reason), `500` if the catalog cannot be loaded.
