# How to enable health checks

Some failures never fire an alert until it is too late, like a volume filling up over days. Health checks read the metrics of your Fly.io services on a schedule and start an investigation when a machine or volume crosses a threshold.

## Prerequisites

- Services with a [`fly` metrics source](connect-metrics.md#flyio). Health checks only run on services whose metrics connector has the presets below (today, Fly.io).
- An LLM configured. Without one, findings are only logged.

## Turn them on

```sh
STROMBOLI_HEALTHCHECK_INTERVAL=1h   # at least 5m; empty or 0 disables them
```

Restart the server. The first run happens a minute after startup, then once per interval.

Each run reads the last 15 minutes of every service and checks:

| Check | Breach |
|---|---|
| `volume_used_percent` | last value ≥ 85% |
| `disk_used_percent` (root filesystem) | last value ≥ 90% |
| `memory_used_percent` | last value ≥ 90% |
| `cpu_percent` | 15-minute average ≥ 90% |

An unhealthy service gets one investigation, with source `healthcheck` and the findings in its alert, posted to Slack like any other. While the same checks keep failing, it is not investigated again for 6 hours.

## Post an all-clear to Slack

By default a healthy run only logs a line. To also post `✅ Health check: all N services operational` to Slack:

```sh
STROMBOLI_HEALTHCHECK_SUMMARY=true
```

It needs [Slack](set-up-slack.md), and is only posted when every checked service is under its thresholds and no check failed.

## Run a single instance

Health checks run on every stromboli instance. Run one instance (as [ADR 0002](../adr/0002-self-hosted-one-instance-per-organisation.md) assumes), or each one will investigate the same breach.
