# Self-hosting stromboli

stromboli is a single Go binary (or container image) plus a Postgres database. One instance serves one organisation ([ADR 0002](adr/0002-self-hosted-one-instance-per-organisation.md)).

## What you need

- **Postgres.** The compose file and CI use Postgres 17 (`pgvector/pgvector:pg17`). Migrations run on startup.
- **An LLM API key** for investigations (Anthropic by default; see [Models](../README.md#models)). Briefs work without one.
- **Network access** from stromboli to your log, metric and deploy sources, to your LLM provider and to Slack. Alert sources must reach stromboli's `/webhooks/*`.
- **Resources:** stromboli is light: it queries logs and metrics where they live and stores only the evidence each investigation used, so a small VM or container is enough.

## Install

### Docker Compose

```sh
git clone https://github.com/openstatushq/stromboli && cd stromboli
cp .env.example .env    # fill in STROMBOLI_SECRET_KEY, STROMBOLI_API_TOKEN, POSTGRES_PASSWORD and an LLM key
docker compose up -d    # pulls ghcr.io/openstatushq/stromboli and starts it with Postgres on :8080
```

Pin a release with `STROMBOLI_VERSION=0.1.0` in `.env` rather than relying on `latest`. Build from source with `docker compose up -d --build`.

### Docker image with your own Postgres

```sh
docker run -d --name stromboli -p 8080:8080 \
  -e STROMBOLI_DB_URL='postgres://stromboli:...@db.internal:5432/stromboli?sslmode=require' \
  -e STROMBOLI_SECRET_KEY=... -e STROMBOLI_API_TOKEN=... -e ANTHROPIC_API_KEY=... \
  ghcr.io/openstatushq/stromboli:0.1.0
```

The image is multi-arch (`linux/amd64`, `linux/arm64`), runs as a non-root user on a distroless base, and listens on `STROMBOLI_ADDR` (default `:8080`).

### Binary

Download the archive for your platform from [Releases](https://github.com/openstatushq/stromboli/releases), check it against `checksums.txt`, and run `stromboli serve` with the same environment variables. The same binary is the CLI.

### Railway

One command deploys stromboli and Postgres: see [deploy/railway](deploy/railway.md).

### Fly.io

`fly.toml` and a setup script deploy stromboli with Fly Managed Postgres: see [deploy/fly](deploy/fly.md).

### Image tags

| Tag | |
|---|---|
| `0.1.0`, `0.1` | a release, or the latest patch of a minor version |
| `latest` | the latest stable release |
| `edge` | the `main` branch: newest, least tested |
| `sha-abc1234` | one commit |

## Production checklist

- [ ] **`STROMBOLI_SECRET_KEY` is set and backed up.** It encrypts connector credentials (AES-256-GCM). Losing or changing it makes stored credentials unreadable, so keep it in your secret manager. Generate it with `openssl rand -base64 32`.
- [ ] **`STROMBOLI_API_TOKEN` is set.** Without it the API is unauthenticated and the server logs a warning. Consider a separate `STROMBOLI_WEBHOOK_TOKEN`, so the token you put in alerting tools can't call the API.
- [ ] **TLS in front.** stromboli speaks plain HTTP: put it behind a reverse proxy or load balancer (Caddy, nginx, Traefik, a cloud LB) that terminates TLS. The proxy must allow HTTP/2 or long-lived HTTP/1.1 responses for `WatchInvestigation` streaming.
- [ ] **Only what's needed is public.** Alert sources need `/webhooks/*`, Slack needs `/slack/interactions` (if you use buttons). The API can stay on your private network or VPN.
- [ ] **Postgres is not the default.** Change `POSTGRES_PASSWORD` before the first `docker compose up`, or use a managed database with `sslmode=require`.
- [ ] **Backups.** All state is in Postgres (catalog, connectors, investigations, actions): back it up like any other database, e.g. `docker compose exec postgres pg_dump -U stromboli stromboli > stromboli.sql`.
- [ ] **Read-only connector credentials.** stromboli only reads from your sources; give it tokens scoped accordingly.
- [ ] **Machine actions stay off** (`STROMBOLI_ACTIONS_ENABLED=false`) unless you need them. If you enable them, set `SLACK_APPROVERS` ([Machine actions](../README.md#machine-actions)).
- [ ] **Health check** on `GET /healthz` (no auth): it returns 200 when the database is reachable.
- [ ] **Logs** go to stdout as structured logs; collect them like your other services.

## Upgrading

1. Read the [release notes](https://github.com/openstatushq/stromboli/releases) for the versions you are skipping.
2. Back up Postgres.
3. Pull the new image (or binary) and restart:

   ```sh
   # in .env: STROMBOLI_VERSION=<new version>
   docker compose pull && docker compose up -d
   ```

Migrations run on startup and only move forward; `stromboli migrate` applies them without starting the server, if you prefer a separate step. The API stays backwards compatible, so CLIs and integrations keep working across upgrades. Investigations that were running during a restart are marked as failed; re-run them with `stromboli investigate`.

To roll back, restore the backup taken before the upgrade and start the previous version: an older version does not know about newer migrations.

## Configuration

Every setting is an environment variable: see [Configuration](../README.md#configuration) and [`.env.example`](../.env.example).
