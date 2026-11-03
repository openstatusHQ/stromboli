# opensre — plan

> **Status (2026-11-02):** milestones 0–3 are built: skeleton, catalog, context (Loki, Axiom and CloudWatch Logs + GitHub), and agent v1 with webhooks, Slack and the audit trail. See README.md. Remaining differences from this plan:
> - `protovalidate` is not wired yet; validation is in Go.
> - The openstatus API connector (as a source of context) is not built; only the openstatus webhook trigger is.
> - Slack steering is not built yet (milestone 5).
> - Investigations **mitigate first, investigate second**: a triage phase proposes a generic mitigation (rollback, restart, scale_up, drain, disable_feature, block_traffic, failover, degrade, none) and publishes it before the root-cause phase. Time to mitigation is stored (`mitigated_at`). Next: a deterministic fast path (no LLM) for obvious cases, such as errors that start right after a deploy → rollback.

An AI SRE agent, in the spirit of OpenAI's SevBot and Google's ProdAgent: it knows every service we run, and when something breaks it gathers context (logs, deploys, metrics, dependencies), forms and checks hypotheses, and reports to humans. It **suggests, it does not execute**.

Source: [The Pragmatic Engineer — AI SRE](https://newsletter.pragmaticengineer.com/p/ai-sre). Lessons from it that shape this plan:

- Most of the value is in the **investigation phase**: collect context faster than a human can.
- **Knowledge graph + memory** is the core asset (Rootly Atlas, incident.io Nexus, ResolveAI HiveMind, Uber Context Graph).
- One agent alone tends to **confirm its first guess** and lose track in long incidents (context rot). ResolveAI landed on Investigator, Verifier, and Communicator roles after three rewrites.
- SevBot and ProdAgent stay read-only by default: actions go through allow-listed tools with policy checks and an audit trail.
- Memory goes stale. Notes need "activated" and "quarantined" states.
- Measuring accuracy is the hard part, so build backtesting early.

---

## 0. Product shape

- **Self-hosted, Metabase-style.** `docker compose up` (opensre + Postgres) gives a working instance (setup wizard once the React UI lands). Teams that already run Postgres can use `docker run -e OPENSRE_DB_URL=postgres://… ghcr.io/openstatushq/opensre` instead. Teams bring their own LLM API key and their own log, metrics, and deploy sources.
- **One instance = one organisation.** As with Metabase, there is no multi-tenancy in the schema. Isolation comes from running separate deployments. That keeps the data model simple, and customer data never leaves their infrastructure apart from LLM calls, which is the trust concern the article raises about vendors.
- **The service catalog is owned by opensre itself.** It is stored in opensre's own database and edited through the API (and later the UI), with YAML import and export so teams who want GitOps can keep a copy in git. It does not depend on any other repo.
- **Storage: Postgres only** (16+, with `pgvector`). Self-hosters already know how to back it up and run it managed (RDS, Cloud SQL, Neon, Supabase). It gives us vector search for similar incidents and runbooks, JSONB for evidence, and safe concurrent writes from parallel sub-investigations. There's one SQL dialect to write and test. A store interface keeps the door open for other databases, but no second adapter is planned unless users ask for one.
- **Config:** connector credentials are set through the API (later the UI) and encrypted at rest with `OPENSRE_SECRET_KEY`. Everything can also be set through env vars or a config file for infra-as-code users.

## 1. Goals / non-goals

**v1 goals**
- Install with one `docker compose up`. Until the React UI exists, setup is done through env vars or `opensre.yaml` plus the CLI and API. Every action the future setup wizard will perform (admin user, LLM key, log source, Slack, services) is already an API endpoint.
- A service catalog: the single source of truth for services, owners, dependencies, log and metric locations, runbooks, and SLOs.
- On an alert (openstatus monitor failure, Alertmanager, or a manual `/sre investigate` in Slack), run an investigation and post a structured report in under 5 minutes.
- Steering: humans can reply in the Slack thread ("check the db", "ignore deploy X") and the agent continues.
- Every tool call and LLM step is logged and replayable.

**Non-goals (v1)**
- Running mitigations (restarts, rollbacks). The agent only *proposes* them, with the exact command.
- Writing code or PRs. It hands off context to a coding agent or an engineer instead.
- Auto-discovering services. The catalog is declared in v1 and discovery comes later.

---

## 2. Architecture

```
            ┌──────────── triggers ────────────┐
            │ openstatus webhook │ Alertmanager │ Slack /sre │
            └──────────────┬───────────────────┘
                           ▼
                  ┌─────────────────┐
                  │  ingest/dedupe  │  → incident record (Postgres)
                  └────────┬────────┘
                           ▼
                ┌──────────────────────┐
                │ investigation engine │
                │  orient → fan-out →  │◀── steering (Slack thread)
                │  verify → report     │
                └───┬────────────┬─────┘
                    │ tools      │ LLM (Anthropic API)
                    ▼            
     ┌───────────────────────────────────────────┐
     │ knowledge layer  │  connectors            │
     │ - service catalog│  - logs  (Loki/ClickHouse/Axiom/CloudWatch)
     │ - dep graph      │  - metrics (Prometheus)│
     │ - runbooks       │  - deploys (GitHub)    │
     │ - past incidents │  - openstatus (monitors, incidents)
     │ - memory notes   │                        │
     └───────────────────────────────────────────┘
                           ▼
              outputs: Slack thread, openstatus incident note, audit log
```

Single Go binary (`opensre serve`) with an embedded web UI, plus a CLI (`opensre catalog import|export|validate`, `opensre investigate --replay <id>`). It ships as one small Docker image (distroless, multi-arch).

### Repo layout

```
cmd/opensre/            main: serve, catalog, investigate, eval subcommands
internal/catalog/       service catalog: CRUD in DB, YAML import/export, validate, graph queries
proto/opensre/v1/       protobuf service definitions (catalog, connectors, investigations, notes, setup)
gen/                    generated Go code (buf generate) – committed
internal/api/           ConnectRPC handlers – everything the UI and CLI need
internal/web/           serves the built React app from go:embed (added once the frontend exists)
web/                    React app (later) – Vite + TypeScript, connect-es client generated from proto/
internal/secrets/       encryption of connector credentials at rest
internal/knowledge/     runbooks, past incidents, memory notes (+ search)
internal/connector/     interfaces + implementations
    logs/  loki.go axiom.go cloudwatch.go clickhouse.go
    metrics/ prometheus.go
    deploys/ github.go
    openstatus/ client.go
internal/trigger/       plain-HTTP webhook handlers (openstatus, alertmanager, grafana, generic), slack events
internal/agent/         investigation loop, roles, tool registry, prompts
internal/llm/           Anthropic client wrapper, token/cost accounting
internal/policy/        tool allow-list, redaction, rate limits
internal/store/         Postgres – services, incidents, steps, notes, audit (behind a Store interface)
internal/report/        Slack blocks, openstatus note formatting
eval/                   recorded incidents + expected root cause
examples/catalog/       sample services.yaml + runbooks for the demo stack
deploy/                 Dockerfile, docker-compose.yml (opensre + Postgres), docker-compose.dev.yml (+ Loki + demo app for dev/eval)
```

**Stack:**
- Go 1.23+ with plain `net/http` (`http.ServeMux`) for the server. Connect handlers, webhooks, and static files all mount on the same mux and port.
- Storage: Postgres via `pgx`, queries generated with `sqlc`, migrations with `goose` (embedded in the binary and run on startup), and `pgvector` for embeddings.
- API first with **ConnectRPC**:
  - Services are defined in protobuf under `proto/`, managed with `buf` (`buf lint`, plus `buf breaking` in CI so self-hosters' scripts don't break on upgrade). Code is generated with `buf generate` using `protoc-gen-go` and `protoc-gen-connect-go`.
  - Example services: `CatalogService` (List/Get/Upsert/Delete/Import/Export/DiscoverServices), `ConnectorService` (Create/Test/List), `InvestigationService` (Start/Get/List, plus **`Watch`, a server stream of steps** for a live timeline in the UI and CLI), `NoteService`, `SetupService`.
  - Connect also serves plain JSON over HTTP POST, so `curl` and scripts work without gRPC tooling. gRPC and gRPC-Web come for free.
  - Validation uses `protovalidate` annotations in the proto files. Authentication is a Connect interceptor checking an API token or session.
  - Health checks use `connectrpc.com/grpchealth`, and API discovery for tools like `grpcurl` uses `connectrpc.com/grpcreflect`.
- **Not everything is Connect:** inbound alert webhooks (openstatus, Alertmanager, Grafana, PagerDuty) and Slack events stay as plain HTTP handlers, because those senders post their own JSON format.
- UI (later): a regular React app (Vite + TS) in `web/`.
  - The client is generated with `@connectrpc/connect-es` / `protoc-gen-es`, and data fetching uses `@connectrpc/connect-query` (TanStack Query).
  - A multi-stage Docker build compiles the app, and the output is embedded in the Go binary with `go:embed`, so it's still a single image.
  - In dev, Vite proxies Connect paths (`/opensre.v1.*`) to Go.
- Integrations: `slack-go/slack` and `anthropic-sdk-go`. Anthropic sits behind an `llm.Provider` interface so OpenAI-compatible providers can be added later, since self-hosters will ask for that.
- Observability: `log/slog` for logs, and OpenTelemetry to trace the agent itself.

---

## 3. Knowledge layer (the "knows all services" part)

### 3.1 Service catalog

Stored in opensre's DB and edited through the API (and later the UI). The YAML format below is the import/export format and the shape of the API. Teams can sync it from git with `opensre catalog import services.yaml` in CI if they want reviewed changes.

```yaml
- name: checker
  description: Runs HTTP/TCP checks from regions, writes results to tinybird
  owners: ["@team-infra"]
  repo: github.com/openstatushq/openstatus
  path: apps/checker
  runtime: fly.io
  tier: 1
  depends_on: [tinybird, turso, qstash]
  logs:
    source: loki                 # name of a configured log connector
    selector: '{service_name="checker"}'
  metrics:
    source: prometheus
    selectors: ['{app="checker"}']
  deploys:
    source: github
    workflow: deploy-checker.yml
  runbooks: [runbooks/checker-high-latency.md]
  slos:
    - name: availability
      target: 99.9
  monitors: [openstatus:1234]   # links alerts → service
```

- Validation runs on every API write and on import: it checks the schema, flags unknown dependencies and cycles, confirms that runbooks exist, and runs a test query against the log selector.
- **Onboarding helper:** `CatalogService.DiscoverServices` asks a log connector for the services it can see and proposes one catalog entry per service, with its selector filled in: Loki `service_name` / `app` label values, Axiom `service.name` / `app` field values per dataset (or one entry per dataset), and one entry per CloudWatch log group. Users then only fill in owners and dependencies. The wizard will use it later. This is cheap discovery without taking on full auto-discovery.
- Load it into memory as a graph (`catalog.Graph`) with `Upstream(svc)`, `Downstream(svc)`, and `BlastRadius(svc)`.
- Map alerts to services through `monitors:` (openstatus IDs) and alert labels (`service=`).

### 3.2 Runbooks
Markdown files referenced from the catalog. Index them by service and by keywords. Use simple full-text search in v1 and embeddings later.

### 3.3 Past incidents
Every investigation is stored with its alert, timeline, hypotheses, verdicts, and the root cause confirmed by a human. This is searchable ("similar incidents on this service").

### 3.4 Memory notes
Short learned facts, scoped to a service:
`"checker latency spikes in syd usually = fly region degradation; check fly status first"`.
- Sources: created by a human (Slack `/sre remember …`) or proposed by the agent after a resolved incident, which needs human approval.
- State: `candidate → active → quarantined`. A note is quarantined when an investigation contradicts it, and each note tracks `last_useful_at`.
- Only `active` notes go into the prompt. This counters the staleness problem described in the article.

---

## 4. Connectors

One small interface per kind of data, so backends can be swapped:

```go
type LogSource interface {
    Query(ctx context.Context, q LogQuery) ([]LogLine, error)          // svc, window, filter, limit
    Patterns(ctx context.Context, q LogQuery) ([]LogPattern, error)    // grouped error signatures + counts
}

type MetricSource interface {
    Range(ctx context.Context, expr string, from, to time.Time, step time.Duration) (Series, error)
}

type DeploySource interface {
    Recent(ctx context.Context, svc string, since time.Time) ([]Deploy, error) // sha, author, PR, diff summary
}
```

Rules:
- **Never return raw firehoses to the LLM.** Connectors pre-aggregate: top error patterns with counts, a comparison between the incident window and the baseline window, and a handful of sample lines. This matters more than any prompt tweak.
- Hard limits on time window, number of rows, and bytes returned.
- Redact secrets, emails, and tokens in `policy` before anything reaches the LLM.

### Logs: query on demand, persist only evidence

opensre does **not** ingest or store logs. It queries the customer's backend when an investigation runs.
- **Stored:** the *evidence* each investigation used. That is the exact query, the time window, the aggregated result (patterns + counts + baseline), and at most about 20 redacted sample lines. It goes into the `steps` table so reports stay verifiable after the backend's retention expires, and it's what `opensre eval` replays.
- **Later, optional:** a background job that stores small per-service *baselines* (top error-pattern fingerprints and hourly counts, a few KB per service) so the agent can say "this pattern is new this week" without scanning days of logs during an incident.
- **Not stored:** raw log streams. No log ingestion endpoint.

### Log backend order

1. **Loki (first).**
   - It's the most common log store among teams who self-host their tooling, which is our audience, and it's free and runs in a single container.
   - Its label model (`{service_name="api"}`) maps one-to-one onto catalog services.
   - Loki 3's `/loki/api/v1/patterns` endpoint gives grouped error signatures for free, and metric queries (`sum by (level) (count_over_time(...))`) make baseline comparisons easy.
   - We can ship it in `docker-compose.yml` for local dev, the demo, and eval fixtures.
2. **ClickHouse with the OpenTelemetry schema (second).**
   - It covers the OTel collector ClickHouse exporter, SigNoz, HyperDX/ClickStack, and Tinybird-style setups.
   - SQL is ideal for "error count vs baseline, grouped by message" queries.
   - Targeting the standard `otel_logs` table means one connector serves many stacks.
3. **Axiom and CloudWatch Logs (built).** Axiom is queried with APL, CloudWatch with Logs Insights (async: start the query, then poll). Both filter on the message field with the same RE2 regexes as Loki.
4. **Elasticsearch/OpenSearch, Datadog** later, ordered by user demand.

The `LogSource` interface stays backend-neutral (service, window, filter, limit), so each new backend is just one file plus a fixture-based test.

Overall build order: **Loki → GitHub deploys → generic alert webhook + openstatus → Prometheus → ClickHouse.**

### Metrics (built)

One Prometheus HTTP API client (`internal/connector/metrics`) serves two kinds: `prometheus` (any compatible backend) and `fly` (Fly.io's managed Prometheus at `api.fly.io/prometheus/<org>`, with presets for machine memory, CPU, throttling, OOM kills, disk, network and proxy HTTP metrics). A service's `metrics.selectors` are PromQL label matchers, substituted for `$selector` in queries. Range queries are summarised per series (min, max, avg, last, baseline average, 12 points) before reaching the model.

---

## 5. Investigation engine

### 5.1 Flow (from the article's "typical steps")

1. **Trigger**: create the incident, find the affected service(s) from the catalog, and open or attach a Slack thread.
2. **Orient** (deterministic code, no LLM): collect the service card, upstream and downstream services, deploys in the last 2h for the service and its dependencies, active memory notes, matching runbooks, the top 3 similar past incidents, and log error patterns compared with baseline. This becomes the **incident brief**.
3. **Hypothesize**: the Investigator reads the brief and proposes up to 3 hypotheses, each with a plan for gathering evidence.
4. **Fan-out**: one sub-investigation per hypothesis runs in parallel (goroutines), each with its own small context and a tool budget (e.g. 10 calls).
5. **Verify**: a separate Verifier call gets *only the evidence*, not the investigator's reasoning, and must try to refute each hypothesis. It returns `supported / refuted / inconclusive` with confidence.
6. **Report**: the Communicator writes the Slack update: likely cause, evidence links, what was ruled out, a suggested mitigation and its command, and confidence.
7. **Steer**: replies in the Slack thread go back into the loop as new instructions.
8. **Close**: when a human resolves the incident, ask for the real root cause (one click) and store it. This becomes eval data and a source of memory candidates.

### 5.2 Tools exposed to the LLM (all read-only in v1)

| tool | backed by |
|---|---|
| `get_service(name)` | catalog |
| `get_dependencies(name, direction)` | catalog graph |
| `query_logs(service, from, to, filter)` | LogSource (aggregated) |
| `log_patterns(service, from, to)` | LogSource |
| `get_metrics(service, metric \| query, since, until)`, `list_metrics(service)` | MetricSource (built) |
| `recent_deploys(service, since)` | DeploySource |
| `get_deploy_diff(sha)` | GitHub (summarized) |
| `monitor_status(id)` / `response_logs(id)` | openstatus |
| `search_incidents(query, service)` | store |
| `read_runbook(path)` | knowledge |

The `policy` package enforces the tool allow-list per role, budgets, and timeouts, and writes every call to the `audit` table.

### 5.3 LLM usage
- Use the `anthropic-sdk-go` tool-use loop. Investigator and Verifier run on the strongest model (Opus); log summarization and the Communicator run on a cheaper model (Sonnet or Haiku). Mixing models also makes the Verifier less likely to share the Investigator's blind spots.
- Keep prompts lean. Put the stable parts (system prompt, catalog summary) first so prompt caching applies.
- Track tokens, cost, and latency per investigation in the store.

---

## 6. Outputs & UX

- **Slack** is the primary surface. Each incident gets one thread. Updates are edited in place rather than spammed. The agent stays quiet unless it has something new (following Rootly's lesson about verbosity).
- **openstatus:** add an incident note with the summary, optionally draft a status-report update for a human to publish.
- **Report shape** (fixed template):
  ```
  🔎 Likely cause (confidence: medium): connection pool exhaustion in api → turso
  Evidence: 4.2k "too many connections" since 14:02 (baseline 0) · deploy a1b2c3 at 13:58 raised worker count 4→16
  Ruled out: fly region outage (all regions affected), tinybird (healthy)
  Suggested mitigation: rollback a1b2c3 → `gh workflow run deploy-api.yml -f sha=9f8e7d`
  Next steps / open questions: …
  ```

---

## 7. Safety & trust

- Read-only credentials for every connector in v1.
- Mitigations are text only. The phase 4 "execute" mode goes through allow-listed actions, requires an explicit Slack approval by an owner, and is audited (as in ProdAgent).
- Redaction runs before anything is sent to the LLM. Use a ZDR-eligible API setup if available.
- Every investigation is fully replayable from the audit log.

---

## 8. Evaluation (start early, not last)

- **Recorded incidents:** snapshot the connector responses for real past incidents into `eval/cases/<id>/` (fixtures) together with the known root cause.
- `opensre eval` replays each case with connectors stubbed by the fixtures, then scores:
  - Root cause in top-1 or top-3 hypotheses (judged by an LLM against the known answer, with spot-checks by humans)
  - Time to report, tokens, and cost
  - Hallucinated evidence (claims not backed by any tool output)
- Run it in CI on prompt or model changes, and track scores over time.
- **Synthetic incidents:** break things in staging on purpose (bad deploy, killed dependency) to grow the eval set.

---

## 9. Milestones

| phase | scope | done when |
|---|---|---|
| **0. Skeleton** (week 1) | Go module, `serve`, Postgres + goose migrations on startup, config, secrets encryption, slog, health endpoint, Dockerfile, docker-compose (opensre + Postgres) and dev compose (+ Loki + demo app) | `docker compose up` boots, CI green |
| **1. Catalog** (week 1–2) | catalog in DB, YAML import/export, graph, validation, runbooks, `CatalogService` (proto + Connect handlers), buf lint/breaking in CI | openstatus's own services entered (via YAML import) as the first real user |
| **2. Context** (week 2–3) | Loki connector + pattern/baseline aggregation, GitHub deploys, openstatus client; `opensre brief <service> --since 1h` prints the orient brief | brief is useful to a human with no LLM involved |
| **3. Agent v1** (week 3–5) | generic webhook + openstatus/Alertmanager/Grafana alert adapters, single Investigator with tools, Slack report, audit log, investigations API | fresh `docker compose up` + config file → real alert → Slack report < 5 min |
| **4. Eval harness** (week 5–6) | fixtures recorder, replay, scoring, 10+ cases | baseline accuracy number |
| **5. Multi-role** (week 6–8) | parallel hypotheses, independent Verifier, Communicator, Slack steering | eval accuracy beats v1 |
| **6. Memory** (week 8–9) | notes lifecycle, `/sre remember`, post-incident note proposals, similar-incident search | notes used in ≥30% of investigations |
| **7. Later** | React UI (setup wizard, catalog editor, investigation timeline), Prometheus + ClickHouse connectors, more LLM providers, Helm chart, auto-discovery (k8s/OTel service graph), alert triage (noise / can wait / page), approved actions, handoff to coding agent | — |

---

## 10. Decisions

- **Product:** self-hosted, one Docker image, one instance per org (Metabase model). No multi-tenancy.
- **Catalog:** lives in opensre (its DB, edited via API, later UI), with YAML import/export for GitOps.
- **First log backend:** Loki, then ClickHouse (OTel schema).
- **Storage:** Postgres only (with pgvector), behind a Store interface. Other adapters only on demand.
- **Frontend:** a regular React app, built later. The backend is API-first with ConnectRPC (protobuf + buf, connect-es/connect-query on the frontend), and the built app is embedded in the Go binary. Webhooks stay plain HTTP.

## 11. Open questions

1. Slack only, or also Discord, Teams, or the openstatus dashboard as an output? Slack-first is assumed.
2. License: AGPL (like Metabase) or Apache-2.0/MIT?
3. Auth for the UI in v1: local users only, or OIDC from the start? Self-hosters at companies will want SSO soon.
4. Telemetry: do we collect anonymous usage stats (opt-out, like Metabase) to learn which connectors to build next?
