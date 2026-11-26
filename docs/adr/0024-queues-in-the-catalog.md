# 0024. Queues in the service catalog

- **Status:** Accepted
- **Date:** 2026-11-26

## Context

A service has one metrics source. A worker that runs on Fly.io and consumes from Pub/Sub can point at its machines or its queue, not both, so the brief of the worker shows no queue and the agent cannot query it. Modelling each subscription as a service does not help: openstatus has 55, one or two per region, and the brief does not show the metrics of dependencies.

Queue incidents also read differently from HTTP ones. On 2026-10-22 openstatus's checkers nacked up to 13,000 messages a minute because the store they write to was rate limiting them. Backlog and throughput looked healthy; restarting or adding consumers would have made it worse. What gave it away was nacks on 39 of 55 subscriptions at once: a shared dependency, not a bad region.

## Decision

- A service lists the queues it consumes from or publishes to:

  ```yaml
  - name: checker
    metrics: {source: fly, selectors: ['app="checker"']}
    queues:
      - name: checks
        source: gcp-metrics
        role: consumer
        subscriptions: ['subscription_id=~"checker-.*"', 'subscription_id!="checker-dead-letter"']
        topics: ['topic_id=~"checker-.*"']
  - name: workflows
    queues:
      - name: checks
        source: gcp-metrics
        role: producer
        topics: ['topic_id=~"checker-.*"']
  ```

  `subscriptions` and `topics` are PromQL label matchers, ANDed, as in `metrics.selectors`: a name pattern alone cannot exclude a dead-letter subscription. A consumer needs `subscriptions`; its `topics` are optional and show what is published to it. A producer needs `topics`.
- `source` is a `gcp_monitoring` connector. Other kinds are refused until they have queue presets.
- Queues do not add edges to the dependency graph: `depends_on` stays the only source of it.
- The brief gets a **Queues** section when a service has queues. Per queue, against the baseline: totals (published, acknowledged, backlog), then for each failure signal (nacks, deliveries per ack, dead letters, oldest message, streaming pulls) **how many subscriptions show it** and the worst few. A consumer counts as disconnected only when it had no open pull for the whole window: streams drop to 0 for a minute in normal operation.
- `get_metrics` and `list_metrics` take an optional `queue`, the queue's name.
- The prompt gets queue guidance: nacks or redeliveries across most subscriptions point at a dependency of the consumers, so reduce load or disable the feature, do not restart or scale up; backlog growing with no open pulls is stopped consumers; backlog growing with more published and flat acks is saturation; nothing published is the producer. p99 ack latency rising alone, with a flat median and no nacks, is usually slow work and not an incident. Purging or seeking a subscription is destructive.
- A metrics response keeps up to 200 series instead of 50, so counts across subscriptions are right. What the model sees stays capped.

Health checks on queues and subscription settings (ack deadline, retry and dead-letter policies) are left to later records.

## Consequences

- One entry covers every region, and the brief tells a shared failure from a local one.
- The catalog format grows by one field. It is additive: existing catalogs and API clients keep working.
- **Downgrading is not safe once a service has queues:** older servers reject unknown fields and fail to load the catalog. Remove `queues` first.
- A brief with a consumer queue makes about 25 more metric queries, in parallel.
- `catalog discover` still proposes one service per subscription, also for those a queue already covers. To revisit.
- Queue names are not shared between services: nothing checks that a producer and a consumer mean the same topics.
