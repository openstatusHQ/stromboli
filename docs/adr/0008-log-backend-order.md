# 0008. Log backends: Loki first

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

We need one log backend to build and test the agent against, then a path to the stores our users run.

## Decision

1. **Loki first.** It is the most common log store among teams who self-host their tooling, it is free and runs in one container (so it ships in the dev compose, the demo and test fixtures), and its labels (`{service_name="api"}`) map one-to-one onto catalog services. Loki 3's patterns endpoint gives grouped error signatures, and metric queries make baseline comparisons easy.
2. **Axiom, CloudWatch Logs and Google Cloud Logging** (built), queried with APL, Logs Insights and the Logging query language. All filter the message field with the same RE2 regexes.
3. **ClickHouse with the OpenTelemetry schema** next: the standard `otel_logs` table covers the OTel collector exporter, SigNoz and HyperDX/ClickStack with one connector.
4. **Elasticsearch/OpenSearch and Datadog** later, by user demand.

The log interface stays backend-neutral (service, window, filter, limit), so each backend is one file plus a fixture-based test. Fly.io has no API for past logs, so Fly users ship logs with `flyio/log-shipper` to one of these stores.

## Consequences

- The demo and tests run fully offline.
- Users of an unsupported store cannot use stromboli for logs until its connector exists.
