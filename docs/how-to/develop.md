# How to develop stromboli

You need Go (the version in [`go.mod`](../../go.mod)) and Docker. For proto changes, [`buf`](https://buf.build/docs/installation); for query or migration changes, [`sqlc`](https://docs.sqlc.dev). Read [CONTRIBUTING.md](../../CONTRIBUTING.md) before opening a pull request.

## Run from source against the demo stack

```sh
docker compose -f docker-compose.dev.yml up -d     # Postgres :55432, Loki :3100, demo app
export STROMBOLI_DB_URL="postgres://stromboli:stromboli@localhost:55432/stromboli?sslmode=disable" STROMBOLI_SECRET_KEY=dev
go run ./cmd/stromboli serve
```

In another terminal, load the example catalog:

```sh
go run ./cmd/stromboli connector add loki loki url=http://localhost:3100
go run ./cmd/stromboli catalog import examples/catalog/services.yaml --runbooks examples/catalog/runbooks
go run ./cmd/stromboli brief api
```

The demo app breaks `db-proxy` after `DEMO_INCIDENT_AFTER` (default `3m`). To see a full investigation, set an LLM key on the server and run `go run ./cmd/stromboli investigate api`. [Tutorial 1](../tutorials/01-first-investigation.md) walks through it.

## Run the checks CI runs

```sh
gofmt -l .        # must print nothing
go vet ./...
go test ./...
buf lint          # if you changed proto/
```

Tests that need Postgres (including the end-to-end agent test) are skipped unless `STROMBOLI_TEST_DB_URL` is set:

```sh
STROMBOLI_TEST_DB_URL="postgres://stromboli:stromboli@localhost:55432/stromboli?sslmode=disable" go test ./...
```

## Regenerate code

Generated code is committed. Regenerate it in the same change as its source:

| You changed | Run | Output |
|---|---|---|
| `proto/stromboli/v1/*.proto` | `buf lint && buf generate` | `gen/stromboli/v1` |
| `internal/store/queries/*.sql` or `internal/store/migrations/*.sql` | `sqlc generate` | `internal/store/db` |
| the MCP tool definitions | `go test ./internal/mcpserver -update` if the golden test fails on purpose | `internal/mcpserver/testdata/tools.golden.json` |

The API must stay backwards compatible (`buf breaking` runs on pull requests), and migrations only move forward: add a new numbered file under `internal/store/migrations` rather than editing one.

## Add a setting

Read it in [`internal/config/config.go`](../../internal/config/config.go), then document it in [`.env.example`](../../.env.example), in the [configuration reference](../reference/configuration.md), and in the Railway declaration [`.railway/railway.go`](../../.railway/railway.go).

## Record a design decision

Bigger changes start as an [ADR](../adr/README.md): copy `docs/adr/template.md` to the next number, open it as **Proposed** in a pull request, and add it to the index.

## Find your way around

[Architecture](../explanation/architecture.md) maps each package to its job.
