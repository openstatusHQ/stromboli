# Deploy on Railway

stromboli's Railway setup is infrastructure as code, in [`.railway/railway.go`](../../.railway/railway.go) (the [Railway Go SDK](https://github.com/railwayapp/railway-go-sdk)). It declares one project with two services:

| Service | Source | Notes |
|---|---|---|
| `stromboli` | `openstatusHQ/stromboli` on GitHub, built from [`deploy/Dockerfile`](../../deploy/Dockerfile) | healthcheck on `/healthz`, restarts on failure |
| `postgres` | Railway Postgres | with its volume. stromboli connects over the private network and runs its migrations on startup |

`/healthz` pings the database, so a deploy that can't reach Postgres never takes traffic.

## Deploy

With the [Railway CLI](https://docs.railway.com/cli) logged in (`railway login`):

```sh
ANTHROPIC_API_KEY=sk-ant-... deploy/railway/setup.sh   # optional argument: the project name, default stromboli
```

The script:

1. creates the Railway project, unless this directory is already linked to one
2. runs `railway config apply`
3. generates `STROMBOLI_SECRET_KEY` and `STROMBOLI_API_TOKEN`, sets `ANTHROPIC_API_KEY`, and redeploys
4. generates the public `*.up.railway.app` domain

It is safe to re-run: it never replaces a secret that is already set.

Deploying a fork? Change the repo in `railway.Github(...)` first.

## Variables

| Variable | In `railway.go` | |
|---|---|---|
| `STROMBOLI_DB_URL` | reference to `postgres`'s `DATABASE_URL` | |
| `PORT` | `8080` | the port the public domain routes to |
| `STROMBOLI_SECRET_KEY` | `railway.Preserve()` | encrypts connector credentials at rest. **Never change it after the first deploy**: stored credentials could no longer be decrypted |
| `STROMBOLI_API_TOKEN` | `railway.Preserve()` | the bearer token for the CLI, the API and webhooks |
| `ANTHROPIC_API_KEY` | `railway.Preserve()` | needed for investigations (briefs work without it) |

`railway.go` declares every variable the server reads, grouped: required, models and provider keys, investigation limits, machine actions, health checks and Slack. Plain settings (`STROMBOLI_MODEL`, `STROMBOLI_MAX_TOOL_CALLS`, `STROMBOLI_ACTIONS_ENABLED`...) are literals, mostly set to the server's defaults: change them in the file, then `railway config apply`. Two differ from the server: machine actions are on (`STROMBOLI_ACTIONS_ENABLED=true`) and [health checks](../../README.md#health-checks) run hourly (`STROMBOLI_HEALTHCHECK_INTERVAL=1h`). Secrets (`STROMBOLI_WEBHOOK_TOKEN`, the other LLM keys, `SLACK_*`) are `railway.Preserve()`.

`railway.Preserve()` keeps the value stored in Railway, so secrets never land in git and `railway config apply` never touches them. An optional secret stays unset until you set it with `railway variable set KEY --stdin --service stromboli`.

## Change the setup

Edit `.railway/railway.go`, then:

```sh
railway config plan    # preview
railway config apply
```

Railway doesn't read `.railway/` during deploys. Pushing to `main` redeploys the code, but configuration changes only take effect on `railway config apply`.

## After deploying

```sh
export STROMBOLI_URL=https://<your-app>.up.railway.app
export STROMBOLI_API_TOKEN=$(railway variable list --service stromboli --kv | sed -n 's/^STROMBOLI_API_TOKEN=//p')
stromboli connector add loki loki url=https://loki.example.com
```

Then continue the [Quickstart](../../README.md#2-connect-your-logs-metrics-and-deploys) from step 2. Alert webhooks go to `https://<your-app>.up.railway.app/webhooks/...`.
