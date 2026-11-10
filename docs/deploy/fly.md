# Deploy on Fly.io

opensre's Fly.io setup is [`fly.toml`](../../fly.toml) at the repository root, plus [`deploy/fly/setup.sh`](../../deploy/fly/setup.sh) for the first deploy. It runs:

| | | Notes |
|---|---|---|
| the `opensre` app | built from [`deploy/Dockerfile`](../../deploy/Dockerfile) | one machine (`shared-cpu-1x`, 512 MB), never auto-stopped, health check on `/healthz` |
| Postgres | [Fly Managed Postgres](https://fly.io/docs/mpg/) or any Postgres you bring | opensre connects over the private network and runs its migrations on startup |

`/healthz` pings the database, so a machine that can't reach Postgres never takes traffic. The machine is never stopped when idle: opensre receives webhooks and runs [health checks](../../README.md#health-checks) on a timer.

## Deploy

With [flyctl](https://fly.io/docs/flyctl/install/) logged in (`fly auth login`), create a database once:

```sh
fly mpg create --name opensre-db --region ams --plan Basic --org <org>
fly mpg list --org <org>   # the cluster ID
```

Then deploy (app names are global on Fly, pick your own):

```sh
FLY_MPG_CLUSTER=<cluster-id> ANTHROPIC_API_KEY=sk-ant-... deploy/fly/setup.sh <app-name> <org>
```

Use `OPENSRE_DB_URL=postgres://...` instead of `FLY_MPG_CLUSTER` to bring your own Postgres.

The script:

1. creates the Fly app, unless it exists
2. attaches the database as the `OPENSRE_DB_URL` secret
3. generates `OPENSRE_SECRET_KEY` and `OPENSRE_API_TOKEN` and sets `ANTHROPIC_API_KEY`
4. runs `fly deploy --ha=false`
5. prints the secrets it generated. **Store them now**: Fly can't show a secret's value again, and `OPENSRE_SECRET_KEY` must never change

It is safe to re-run: it never replaces a secret that is already set.

## Variables

Plain settings are in `[env]` in `fly.toml`: models, investigation limits, machine actions and health checks. They are set to the server's defaults, except two: machine actions are on (`OPENSRE_ACTIONS_ENABLED=true`) and health checks run hourly (`OPENSRE_HEALTHCHECK_INTERVAL=1h`). Change them in the file, then deploy.

Secrets are set in Fly, never in the file:

| Secret | |
|---|---|
| `OPENSRE_DB_URL` | set by `fly mpg attach`, or by the script from your URL |
| `OPENSRE_SECRET_KEY` | encrypts connector credentials at rest. **Never change it after the first deploy**: stored credentials could no longer be decrypted |
| `OPENSRE_API_TOKEN` | the bearer token for the CLI, the API and webhooks |
| `ANTHROPIC_API_KEY` | needed for investigations (briefs work without it) |
| optional: `OPENSRE_WEBHOOK_TOKEN`, other provider keys, `SLACK_BOT_TOKEN`, `SLACK_CHANNEL`, `SLACK_SIGNING_SECRET`, `SLACK_APPROVERS` | |

Set one with `fly secrets set KEY=value --app <app-name>`. That restarts the machine.

## Change the setup

Edit `fly.toml`, then:

```sh
fly deploy --app <app-name> --ha=false
```

Keep one machine: the health checks run on every machine, and alert dedupe assumes a single instance. `--ha=false` stops `fly deploy` from adding a second one.

## After deploying

```sh
export OPENSRE_URL=https://<app-name>.fly.dev
export OPENSRE_API_TOKEN=<the token the script printed>
opensre connector add loki loki url=https://loki.example.com
```

Then continue the [Quickstart](../../README.md#2-connect-your-logs-metrics-and-deploys) from step 2. Alert webhooks go to `https://<app-name>.fly.dev/webhooks/...`.

To have opensre investigate the Fly apps of your organization, add a `fly` connector with a [read-only token](../../README.md#flyio): it is separate from the app opensre itself runs in.
