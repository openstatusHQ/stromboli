# Contributing to opensre

Thanks for helping! Bug reports, new connectors, docs fixes and design discussions are all welcome.

## Before you start

- **Bugs:** [open an issue](https://github.com/openstatushq/opensre/issues/new) with the version (`opensre --version`), how you run it, what you expected and what happened.
- **Features and new connectors:** open an issue first so we can agree on the approach before you write code. Bigger design changes start as an [ADR](docs/adr/README.md) in **Proposed** status.
- **Security issues:** don't open a public issue, see [SECURITY.md](SECURITY.md).

## Development setup

You need Go (the version in [`go.mod`](go.mod)) and Docker. Optional: [`buf`](https://buf.build/docs/installation) for proto changes and [`sqlc`](https://docs.sqlc.dev) for query changes.

```sh
docker compose -f docker-compose.dev.yml up -d     # Postgres :55432, Loki :3100, a demo app with a fake incident
export OPENSRE_DB_URL="postgres://opensre:opensre@localhost:55432/opensre?sslmode=disable" OPENSRE_SECRET_KEY=dev
go run ./cmd/opensre serve
```

See [Development](README.md#development) in the README to load the example catalog and run an investigation against the demo app.

## Checks

CI runs these; run them before you push:

```sh
gofmt -l .                   # must print nothing
go vet ./...
go test ./...                # set OPENSRE_TEST_DB_URL=postgres://... to also run the database tests
buf lint                     # if you changed proto/
```

Generated code is committed. Regenerate it in the same PR when you change its source:

```sh
buf generate                 # after editing proto/
sqlc generate                # after editing internal/store/queries or migrations
```

## Guidelines

- **Keep PRs focused.** One change per PR, with tests for new behavior and bug fixes.
- **Respect the design decisions** in [docs/adr](docs/adr): opensre is read-only by default ([0006](docs/adr/0006-suggest-dont-execute.md)), queries data where it lives ([0007](docs/adr/0007-query-logs-on-demand.md)) and stores everything in Postgres ([0004](docs/adr/0004-postgres-only-storage.md)).
- **The API stays backwards compatible.** Self-hosters upgrade on their own schedule; `buf breaking` runs on every PR. Database migrations only move forward and must be safe to run on startup.
- **The CLI follows [clig.dev](https://clig.dev)** ([0015](docs/adr/0015-cli-follows-clig.md)): data on stdout, messages on stderr, `--help` with examples.
- **No secrets in logs, errors or reports.** Connector credentials are encrypted at rest; keep them that way.
- **Document new settings** in [`.env.example`](.env.example) and the [Configuration](README.md#configuration) table.

## License

By contributing, you agree that your contributions are licensed under the [MIT license](LICENSE), like the rest of the project.
