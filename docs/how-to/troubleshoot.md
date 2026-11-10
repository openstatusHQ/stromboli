# How to troubleshoot stromboli

Start with the server logs: stromboli logs a warning for every setting that is half-configured, and for every alert it rejects or ignores. Run the CLI with `--debug` to see its requests.

## The server won't start

| Error | Fix |
|---|---|
| `STROMBOLI_DB_URL is required` / `STROMBOLI_SECRET_KEY is required` | set them; generate the key with `openssl rand -base64 32` |
| `STROMBOLI_ACTIONS_ENABLED requires STROMBOLI_API_TOKEN` (or `STROMBOLI_MCP_ENABLED`) | set `STROMBOLI_API_TOKEN` |
| `STROMBOLI_HEALTHCHECK_INTERVAL: … is too short, use at least 5m` | use `5m` or more, or leave it empty |
| `STROMBOLI_ACTION_TTL: invalid duration` | a Go duration such as `30m` or `1h` |
| a database connection error | check the URL and that Postgres is reachable; `stromboli migrate` tries the connection and the migrations without starting the server |

## Investigations don't start

| Symptom | Cause and fix |
|---|---|
| `no LLM is configured` | the startup log says `LLM is not configured` with the reason: set the provider's key, or fix the model id ([Choose models](choose-models.md)) |
| `service "x" is not in the catalog` | the name in the alert or command does not match a catalog entry: `stromboli catalog list` |
| webhook answers `401`, log says `webhook rejected` | wrong token: compare with `STROMBOLI_WEBHOOK_TOKEN` (or `STROMBOLI_API_TOKEN`) |
| webhook answers `ignored: no matching service in the catalog` | Alertmanager/Grafana: the alert needs a `service`, `service_name`, `app`, `job` or `container` label equal to the catalog name. openstatus: add `openstatus:<monitor id>` to the service's `monitors` |
| webhook answers `deduplicated` | the alert was merged into a running investigation, or one that finished within `STROMBOLI_DEDUPE_WINDOW`. Send a resolved alert, or wait out the window |

## Investigations fail or say little

| Symptom | Cause and fix |
|---|---|
| `investigation timed out after 8m` | raise `STROMBOLI_INVESTIGATION_TIMEOUT`, or use a faster triage model |
| `gave up after N tool calls without a report` | the model kept calling tools; try a stronger investigate model |
| `the model hit the output token limit` / `declined to continue` | try another model |
| failed with nothing else, right after a restart | investigations running during a restart are marked failed; re-run with `stromboli investigate` |
| the brief has no logs or metrics | `stromboli catalog validate --check-connectors`, then fix the selector or `stromboli connector test <name>` |
| `decrypt failed: was STROMBOLI_SECRET_KEY changed?` | the secret key differs from the one that encrypted the connectors: restore it, or re-add each connector with `--replace` |

Every investigation keeps its audit trail: `stromboli show <id>` shows each step, and failed tool calls are marked `✗` with their error.

## Nothing appears in Slack

See [Set up Slack: check it](set-up-slack.md#5-check-it). Also check the startup log: `Slack is not configured` means `SLACK_BOT_TOKEN` or `SLACK_CHANNEL` is missing.

## Slack buttons don't show or don't work

| Log or symptom | Fix |
|---|---|
| `SLACK_SIGNING_SECRET is set but Slack buttons need …` | buttons also need `SLACK_BOT_TOKEN`, `SLACK_CHANNEL` and `STROMBOLI_ACTIONS_ENABLED=true` |
| clicking does nothing | Slack cannot reach `https://<stromboli>/slack/interactions`, or the Request URL is wrong |
| `🚫 You are not allowed to decide stromboli actions` on click | the user is not in `SLACK_APPROVERS` |

## An action failed or is stuck

`stromboli actions show <id>` prints the result machine by machine and the error. A restart, start or clone that does not reach `started` within 60 seconds stops the action at that machine. Actions that were running when the server restarted are marked failed: check their machines with `fly machine list -a <app>`.

## The CLI can't reach the server

| Error | Fix |
|---|---|
| connection refused | `--url` / `STROMBOLI_URL` is wrong, or the server is down: `curl $STROMBOLI_URL/healthz` |
| `invalid or missing API token` | set `STROMBOLI_API_TOKEN` or `--token-file` |
| streaming (`investigate`, `show --follow`) stops early | a proxy buffers or cuts long responses: allow HTTP/2 or long-lived HTTP/1.1 responses |
