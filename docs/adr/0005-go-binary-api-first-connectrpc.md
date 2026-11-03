# 0005. A single Go binary, API first with ConnectRPC

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

opensre ships to self-hosters, so it should be one small image that is easy to run and upgrade. A CLI, a future web UI and users' scripts all need the same operations, and those scripts must not break on upgrade ([0002](0002-self-hosted-one-instance-per-organisation.md)).

## Decision

- **One Go binary**, `opensre`, is both the server (`opensre serve`) and the CLI. It ships as one distroless, multi-arch Docker image. The server uses plain `net/http`; Connect handlers, webhooks and static files share one mux and port.
- **API first with ConnectRPC.** Services are defined in protobuf under `proto/opensre/v1` (`CatalogService`, `ConnectorService`, `InvestigationService`, `ActionService`), managed with `buf`: `buf lint`, and `buf breaking` in CI. Generated code in `gen/` is committed. The same endpoints speak Connect, gRPC, gRPC-Web and plain JSON over POST, so `curl` works. Reflection is on for `grpcurl`.
- Everything a future setup wizard does is already an API call.
- **Webhooks and Slack stay plain HTTP**, because those senders post their own JSON format.
- The web UI, when it lands, is a React app (Vite + TypeScript, `connect-es` and `connect-query`), built in a multi-stage Docker build and embedded with `go:embed`.
- Logs use `log/slog`; the agent itself is traced with OpenTelemetry.

## Consequences

- The CLI is a client of the public API, so the API is always complete.
- `buf breaking` makes compatibility a CI check instead of a review comment.
- Validation is in Go for now; `protovalidate` annotations are not wired yet.
