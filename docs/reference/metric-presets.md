# Metric presets reference

A preset is a named PromQL query a metrics connector knows. The agent passes it as `metric` to its `get_metrics` tool; presets marked **brief** are also shown in every brief of the service. The `prometheus` kind has no presets: the agent uses `list_metrics` and writes PromQL.

Every series comes back as min, max, avg and last over the window, its average over the baseline (the preceding window of the same length) and 12 points, at most 10 series per query.

## fly

From Fly's [managed Prometheus](https://fly.io/docs/monitoring/metrics/). Machine metrics are per `instance` (the machine ID) and `region`.

| Preset | Unit | Brief | |
|---|---|---|---|
| `memory_used_percent` | % | ✓ | memory in use (total − available) as a percentage of total, per instance |
| `memory_used_bytes` | bytes | | memory in use, per instance |
| `swap_used_bytes` | bytes | | swap in use, per instance; growing swap usually means memory pressure |
| `oom_killed` | | ✓ | 1 when the instance's last exit was an out-of-memory kill |
| `cpu_percent` | % | ✓ | CPU busy time (all modes but idle), per instance |
| `cpu_throttled_seconds_per_second` | s/s | ✓ | CPU time throttled after exhausting the CPU quota; above 0 means CPU-starved |
| `load_average` | | | 1-minute load average, per instance |
| `disk_used_percent` | % | ✓ | root filesystem in use, per instance (reset from the image on restart) |
| `volume_used_percent` | % | ✓ | Fly volume space in use, per volume (`id` is the volume ID); survives restarts |
| `net_recv_bytes_per_second` | bytes/s | | network bytes received, per instance |
| `net_sent_bytes_per_second` | bytes/s | | network bytes sent, per instance |
| `http_requests_per_second` | req/s | ✓ | HTTP responses served by the Fly proxy, per region |
| `http_5xx_per_second` | req/s | ✓ | HTTP 5xx responses served by the Fly proxy, per region |
| `http_p95_seconds` | s | ✓ | p95 HTTP response time at the Fly proxy, per region |
| `concurrency` | | | concurrent connections or requests routed to each instance |
| `instances_up` | | | machines reporting metrics, per region |

Source metrics: `fly_instance_memory_*`, `fly_instance_exit_oom`, `fly_instance_cpu`, `fly_instance_cpu_throttle`, `fly_instance_load_average`, `fly_instance_filesystem_blocks*`, `fly_volume_used_pct`, `fly_instance_net_*_bytes`, `fly_app_http_responses_count`, `fly_app_http_response_time_seconds`, `fly_app_concurrency`, `fly_instance_up`.

[Health checks](../how-to/enable-health-checks.md) use `volume_used_percent`, `disk_used_percent`, `memory_used_percent` and `cpu_percent`.

## gcp_monitoring

Which presets apply depends on the resource label in the service's `metrics.selectors`.

### Pub/Sub (`subscription_id`)

| Preset | Unit | Brief | |
|---|---|---|---|
| `pubsub_backlog_messages` | | ✓ | messages not yet acknowledged, per subscription |
| `pubsub_oldest_unacked_seconds` | s | ✓ | age of the oldest unacknowledged message |
| `pubsub_acks_per_second` | msg/s | ✓ | messages acknowledged per second: consumer throughput |
| `pubsub_sent_per_second` | msg/s | | messages delivered per second, redeliveries included |
| `pubsub_expired_ack_deadlines_per_second` | msg/s | ✓ | messages whose ack deadline expired per second |
| `pubsub_dead_letter_per_second` | msg/s | ✓ | messages forwarded to the dead-letter topic per second |
| `pubsub_nacks` | | | negative acknowledgements |

### Cloud Tasks (`queue_id`)

| Preset | Unit | Brief | |
|---|---|---|---|
| `tasks_queue_depth` | | ✓ | tasks waiting in the queue |
| `tasks_attempts_per_second` | req/s | ✓ | task attempts per second, by response code |
| `tasks_attempt_delay_p95_seconds` | s | | p95 delay between schedule time and attempt |

### Cloud Run (`service_name`)

| Preset | Unit | Brief | |
|---|---|---|---|
| `http_requests_per_second` | req/s | ✓ | requests per second, per revision |
| `http_5xx_per_second` | req/s | ✓ | 5xx responses per second, per revision |
| `http_p95_seconds` | s | ✓ | p95 request latency, per revision |
| `instances` | | ✓ | container instances, per revision and state (active, idle) |
| `cpu_utilization_p95_percent` | % | ✓ | p95 CPU utilization, per revision |
| `memory_utilization_p95_percent` | % | ✓ | p95 memory utilization, per revision |
| `startup_latency_p95_seconds` | s | | p95 container startup latency (cold starts), per revision |

`list_metrics` lists a project's active metrics under their PromQL names, e.g. `pubsub_googleapis_com:subscription_num_undelivered_messages`.
