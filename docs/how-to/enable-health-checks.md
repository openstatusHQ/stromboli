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

A service that reported no metrics at all in the last 15 minutes is investigated too: its machines are stopped, or its selectors match nothing. The investigation checks the machines' state and, if they are stopped and the service should be running, proposes starting them for a human to approve ([machine actions](enable-machine-actions.md)). It is deduplicated apart from threshold breaches, so a service that stays silent is investigated once every 6 hours. An app that scales to zero on purpose will be investigated too; remove its `fly` metrics source from the catalog to skip it.

## Post a summary to Slack

By default a healthy run only logs a line. To also post `✅ All services operational` to Slack, and a warning listing the services that could not be checked:

```sh
STROMBOLI_HEALTHCHECK_SUMMARY=true
```

It needs [Slack](set-up-slack.md). The all-clear is only posted when every checked service is under its thresholds, reported metrics, and no check failed.

When some services could not be checked and nothing else reports them (a metrics query failed, or a service had no metrics and its investigation could not start, for example without an LLM), the run posts one warning instead, with the reason for each:

```
⚠️ Health check: could not check 1 service
• api: cpu_percent: context deadline exceeded
```

The warning is posted on every run until the service can be checked again, so a short interval means one message per run.

## Run a single instance

Health checks run on every stromboli instance. Run one instance (as [ADR 0002](../adr/0002-self-hosted-one-instance-per-organisation.md) assumes), or each one will investigate the same breach.
