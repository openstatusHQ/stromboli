# Tutorial 1: your first investigation, locally

In this tutorial you run stromboli on your own machine next to a small fake system. The system has four services (`web`, `api`, `checker` and `db-proxy`) that write logs to Loki. Three minutes after it starts, `db-proxy` runs out of database connections and `api` starts failing. You will watch stromboli notice what changed, propose a mitigation, and explain the root cause.

You don't need Slack, a cloud account or any real infrastructure. You need:

- [Go](https://go.dev/dl/) (the version in [`go.mod`](../../go.mod)) and [Docker](https://docs.docker.com/get-docker/) with Compose
- an API key for an LLM. This tutorial uses Anthropic (`ANTHROPIC_API_KEY`). Any provider in [Choose models](../how-to/choose-models.md) works too.
- a clone of the repository:

  ```sh
  git clone https://github.com/openstatushq/stromboli && cd stromboli
  ```

## 1. Start the demo stack

The dev Compose file starts Postgres, Loki and the demo app:

```sh
docker compose -f docker-compose.dev.yml up -d
```

Check that the three containers are running:

```sh
docker compose -f docker-compose.dev.yml ps
```

The demo app is now writing healthy logs to Loki every second. The fake incident starts **three minutes** after it started, so keep going.

## 2. Start the stromboli server

Open a new terminal in the repository. The server needs a database URL and a secret key (any string works locally), plus your LLM key:

```sh
export STROMBOLI_DB_URL="postgres://stromboli:stromboli@localhost:55432/stromboli?sslmode=disable"
export STROMBOLI_SECRET_KEY=dev
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/stromboli serve
```

You will see log lines like these:

```text
level=WARN msg="STROMBOLI_API_TOKEN is not set: the API is unauthenticated, do not expose this instance"
level=INFO msg="Slack is not configured (SLACK_BOT_TOKEN, SLACK_CHANNEL): reports are available through the API only"
level=INFO msg="stromboli listening" addr=:8080 triage_model=anthropic:claude-opus-5-5 investigate_model=anthropic:claude-opus-5-5
```

Both warnings are expected: without an API token anyone who can reach port 8080 can use the API, which is fine on your laptop. Leave this terminal running.

## 3. Connect Loki

Open a third terminal in the repository. Everything from now on uses the CLI, which is the same binary talking to the server at `http://localhost:8080`.

Tell stromboli where the logs are by adding a **connector** named `loki`, of kind `loki`:

```sh
go run ./cmd/stromboli connector add loki loki url=http://localhost:3100
```

```text
Connector "loki" saved.
Connector "loki" is reachable.
Next: propose catalog entries from it with `stromboli catalog discover loki > services.yaml`
```

stromboli tested the connection when you added it. It does not copy your logs anywhere: it will query Loki only when it needs to.

## 4. Describe the services

stromboli needs to know what your services are, how they depend on each other and where their logs live. This is the **service catalog**. The repository has one ready for the demo:

```sh
go run ./cmd/stromboli catalog import examples/catalog/services.yaml --runbooks examples/catalog/runbooks
```

```text
Imported 4 services and 2 runbooks; deleted 0 services.
```

List what you imported:

```sh
go run ./cmd/stromboli catalog list
```

```text
NAME      TIER  OWNERS          DEPENDS ON  SOURCES
api       1     @team-backend   db-proxy    logs=loki
checker   1     @team-infra     -           logs=loki
db-proxy  1     @team-infra     -           logs=loki
web       2     @team-frontend  api         logs=loki
```

Notice the `DEPENDS ON` column: `web` calls `api`, which calls `db-proxy`. stromboli uses this graph to look at dependencies when something breaks.

Open [`examples/catalog/services.yaml`](../../examples/catalog/services.yaml) to see what one entry looks like. The `logs.selector` of `api` is `{service_name="api"}`, the Loki stream selector that finds its logs.

## 5. Read a brief

Before involving the LLM, look at what stromboli gathers on its own. This is the **brief**, the context an on-call engineer would look at first:

```sh
go run ./cmd/stromboli brief api --since 15m
```

The brief is Markdown. Read through its sections: the service card, its dependencies and blast radius, log error patterns compared with the previous 15 minutes, recent changes, runbooks and similar past investigations. Some sections say there is nothing to show: there are no metrics or deploys in this demo.

Building a brief costs nothing: no LLM is called.

## 6. Wait for the incident

Run the brief again until the logs section shows new error patterns from `api`, such as `query failed err="pq: sorry, too many clients already"`. If you started the stack more than three minutes ago, they are already there.

```sh
go run ./cmd/stromboli brief api --since 15m
```

Also look at the brief of `db-proxy`:

```sh
go run ./cmd/stromboli brief db-proxy --since 15m
```

You can probably guess what is going on. Now let stromboli work it out.

## 7. Start an investigation

```sh
go run ./cmd/stromboli investigate api --description "5xx on /v1/monitors"
```

The investigation streams as it happens:

1. `· built brief for api`: the same brief you just read.
2. Lines starting with `→`: each read-only tool the agent calls, for example `log_timeline` or `log_patterns` on `db-proxy`.
3. A **mitigation** block, after a handful of tool calls: the fastest safe way to stop user impact, with the exact command, how to verify it, how to undo it and its risk.
4. `· investigating the root cause…`, more tool calls, then the **report**: summary, likely cause, confidence, evidence, what was ruled out, the long-term fix and next steps.

It takes a minute or two. At the end, stromboli prints how many tokens it used.

Read the mitigation and the report. stromboli likely found that `db-proxy`'s pool is exhausted (`active=100 max=100`), that `api`'s errors are a consequence, and, from the `db-proxy-pool.md` runbook, that restarting `db-proxy` first is a bad idea.

> [!TIP]
> If you press Ctrl-C while it runs, only the CLI stops watching: the investigation keeps running on the server. Resume with `go run ./cmd/stromboli show <id> --follow`.

## 8. Look at the audit trail

Every investigation is stored. List them:

```sh
go run ./cmd/stromboli investigations
```

Then show yours, with every step it took:

```sh
go run ./cmd/stromboli show <investigation-id>
```

This is the audit trail: the brief, each tool call with its input, each model turn, the mitigation and the report.

## 9. Clean up

Stop the server with Ctrl-C, then the stack:

```sh
docker compose -f docker-compose.dev.yml down
```

## What you learned

- stromboli is one binary: `serve` runs the server, every other command is a client.
- **Connectors** give it read access to your data, which it queries on demand.
- The **catalog** tells it what your services are, how they depend on each other and where their data lives.
- An investigation starts from a deterministic **brief**, publishes a **mitigation** first, then writes a root-cause **report**.

## Next

- [Tutorial 2: connect a real service](02-connect-a-real-service.md)
- Why the mitigation comes before the root cause: [Mitigate first, investigate second](../explanation/mitigate-first.md)
