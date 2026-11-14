---
name: stromboli
description: Operate a deployed stromboli instance (the self-hosted AI SRE agent from openstatus) with its CLI. Use this whenever the user wants to check what is going on with one of their services, look at error patterns or a brief, start or follow an incident investigation, read a past investigation's mitigation or report, approve or reject a machine action stromboli proposed, connect a log/metrics/deploy source (Loki, Axiom, CloudWatch, Google Cloud, Prometheus, Fly.io, GitHub, Vercel), discover or edit the service catalog, wire alerts into stromboli's webhooks, hook stromboli up to Claude Code over MCP, or fix "cannot reach the stromboli server" / token errors. Trigger even if the user just says "is api ok?", "why is checker throwing 500s", "what did stromboli find", or names an investigation or action ID, as long as they run stromboli.
---

# Operating stromboli

stromboli is a self-hosted AI SRE agent. A server (run by the user's team, usually on Fly.io, Railway or Docker) holds the service catalog and connectors, receives alerts, and runs investigations: it proposes a mitigation first, then finds the root cause. The `stromboli` CLI is a client of that server. Everything below is done through the CLI against the user's existing instance.

## 1. Connect to the instance

```sh
stromboli --version                      # installed? if not: brew install openstatushq/stromboli/stromboli
export STROMBOLI_URL=https://stromboli.example.com
export STROMBOLI_API_TOKEN=...           # or pass --token-file <path> on every command
curl -s "$STROMBOLI_URL/healthz"         # prints: ok
stromboli catalog list                   # proves the URL and token work
```

Ask the user for the URL if `STROMBOLI_URL` is unset. Don't ask them to paste the token into the chat. Have them export it in their shell, or point you at a token file. The CLI defaults to `http://localhost:8080` when `STROMBOLI_URL` is unset, which is almost never what an end user wants.

Binaries for other platforms are on https://github.com/openstatushq/stromboli/releases.

## 2. Check what the installed version supports

The CLI evolves quickly before 1.0, and the user's binary may be older than the docs. **`stromboli <command> --help` is the source of truth.** Check it before using a flag you aren't sure about. Known gap: **v0.1.0 has no `catalog set` or `catalog rm`, and `catalog discover` has no `--import`/`--owner`.** On v0.1.0, change the catalog with export → edit → import (section 6). An unknown command exits 2 with `unknown command "set" for "stromboli catalog"`.

## 3. How the CLI behaves when you run it

You run commands without a terminal, so the CLI behaves as if `--no-input` were set:

- **Prompts fail instead of waiting.** A secret given as a bare key (`token`), a confirmation, or the approver's name all error out and name the flag to pass.
- **Destructive commands need `--yes`:** `connector rm`, `catalog rm`, `catalog import --prune` when it would delete something, `actions approve`. Without it you get e.g. `refusing to delete connector "loki" without confirmation: re-run with --yes`. Treat that message as your cue to confirm with the user, not to retry with `--yes`. These commands delete production config or act on live machines.
- **`catalog discover` prints YAML** instead of its interactive picker, because stdout isn't a terminal. That's useful: review it, then `catalog import` it.
- **Data on stdout, messages on stderr.** Add `--json` when you want to parse (`jq`), `-q` to cut chatter.
- **Exit codes:** `0` ok, `1` error, `2` bad usage (followed by `Run '<cmd> --help' for usage.`), `130` interrupted.

For anything that genuinely needs the user's hands (an OAuth browser login, typing a secret), give them the exact command to run themselves. In Claude Code they can type `! <command>` so the output lands in the conversation.

## 4. Look at a service, then investigate

Start with the **brief**. It's what stromboli gathers before the model starts: the catalog entry, dependencies and blast radius, log volume, **new error patterns vs the previous window**, metrics, recent deploys, runbooks and similar past investigations. It uses no LLM, costs nothing and posts nothing, so use it freely.

```sh
stromboli catalog list                    # exact service names
stromboli brief api --since 30m           # default 1h, max 168h
```

Read the brief to the user in plain terms: what's new, since when, and whether it lines up with a deploy or a dependency.

An **investigation** spends LLM tokens, posts to the team's Slack channel if Slack is configured, and can take several minutes (server default timeout 8m). Confirm with the user before starting one, unless they asked for it outright. Start it detached so a long run doesn't hit your command timeout, then follow it:

```sh
id=$(stromboli investigate api --description "5xx on /v1/monitors since 14:05" --no-watch)
stromboli show "$id" --follow             # streams steps until done. Run with a long timeout or in the background
```

The stream shows `→ tool input (duration)` per tool call (`✗` on error), model notes prefixed `·`, then the **mitigation** (published early), the **report** and token counts. Lead your summary with the mitigation, then the root cause.

Past investigations:

```sh
stromboli investigations --limit 10       # ID, CREATED, STATUS, SERVICES, TITLE
stromboli show <id>                       # header, every step, mitigation, report
stromboli show <id> --json                # for jq
```

## 5. Machine actions: a human decides

If the server has `STROMBOLI_ACTIONS_ENABLED=true`, investigations can *propose* Fly machine actions (restart, clone, cordon…). Nothing runs until a named human approves it. That's the core of stromboli's safety model, and the approval records who decided.

```sh
stromboli actions list --status proposed
stromboli actions show <action-id>        # what it does, on which machines, expiry
```

Show the user exactly what the action does, then approve or reject **only on their explicit go-ahead for that specific action**. Record **their** name, not yours:

```sh
stromboli actions approve <action-id> --as "<user's name>" --note "<why>" --yes   # runs it now, exits 1 if it failed
stromboli actions reject  <action-id> --as "<user's name>" --note "<why>"
```

Proposed actions expire (server default 30m). `approve` fails if the action is no longer `proposed`.

## 6. The service catalog

Each catalog entry says where a service's logs, metrics and deploys live, plus owners, tier, dependencies, runbooks and SLOs. Investigations only work on services in the catalog, matched by name.

```sh
stromboli catalog discover loki > new.yaml      # entries for services the connector sees and the catalog lacks
stromboli catalog import new.yaml --dry-run     # validate only
stromboli catalog import new.yaml               # create/update, never deletes without --prune
stromboli catalog validate --check-connectors   # also test each selector against its connector
```

**Editing (works on every version):** export, edit the YAML, import.

```sh
stromboli catalog export --runbooks runbooks > services.yaml
# edit services.yaml / runbooks/*.md
stromboli catalog import services.yaml --runbooks runbooks --dry-run
stromboli catalog import services.yaml --runbooks runbooks
```

**Newer CLIs** also have `catalog set <name> --owner @team --add-depends-on postgres --tier 1` and `catalog rm <name>`.

Ask whether the team keeps the catalog in git. If they do, change the file there too, otherwise their next `catalog import` reverts your `set`/`rm`. Only use `--prune` when the user means "make the server match this file exactly". It deletes every service not in the file.

Field reference for entries: [references/connectors.md](references/connectors.md) has selectors per connector kind. The full schema is in the stromboli repo at `docs/reference/catalog.md`.

## 7. Connectors

```sh
stromboli connector list                       # secrets shown as ••••
stromboli connector add <name> <kind> key=value... [--replace]   # saves, then tests
stromboli connector test <name>
```

Never put a secret inline (`token=ghp_...`). It lands in shell history and the transcript, and the CLI warns about it. Use `token=@/path/to/file`, or `token=-` with the value piped on stdin (one key per command). If the user has the secret but no file, hand them the command to run themselves with the bare key (`token`), which prompts with hidden input.

`axiom` and `vercel` log in with OAuth in a browser: always hand `stromboli connector add ...` (or `connector login <name>` when a login expired) to the user to run.

Kinds, required keys and selectors: [references/connectors.md](references/connectors.md). After adding a log or metrics connector, run `catalog discover <name>`.

## 8. Alerts and MCP

Alerts start investigations on their own once a source points at the server:

| Source | Endpoint |
|---|---|
| openstatus webhook (header `Authorization: Bearer <token>`) | `POST /webhooks/openstatus` |
| Alertmanager | `POST /webhooks/alertmanager?token=<token>` |
| Grafana alerting | `POST /webhooks/grafana?token=<token>` |
| anything else: `{"service","title","fingerprint","details"}` | `POST /webhooks/generic` |

The token is the server's `STROMBOLI_WEBHOOK_TOKEN` (or the API token). **The webhook answers 202 even when no investigation started.** Read the body: `{"alerts":[{"title":…,"status":"error: no LLM is configured …"}]}`, `ignored: no matching service in the catalog`, or `deduplicated`. A test alert starts a real investigation (tokens + Slack), so confirm first.

To use stromboli from Claude Code directly (catalog, briefs, investigations as MCP tools, with no action approval):

```sh
claude mcp add stromboli -e STROMBOLI_URL="$STROMBOLI_URL" -e STROMBOLI_API_TOKEN=... -- stromboli mcp
```

## 9. When something fails

| Error | What to do |
|---|---|
| `cannot reach the stromboli server at http://localhost:8080` | `STROMBOLI_URL` unset or wrong. Set it, and check `curl $STROMBOLI_URL/healthz` |
| `the server rejected the API token (invalid or missing API token)` | export `STROMBOLI_API_TOKEN` (or `--token-file`) with the server's token |
| `404 Not Found … may be running an older version of stromboli` | CLI newer than the server. Use commands the server has, or ask the team to upgrade it |
| `unknown command "…"` / `unknown flag` (exit 2) | CLI older than the docs. Check `--help` and use the export/edit/import path |
| `no LLM is configured (STROMBOLI_MODEL and the provider's API key)` | server-side config. The team must set a provider key and restart. Briefs still work |
| `service "x" is not in the catalog` | use a name from `catalog list`, or add the service |
| `investigation <id> not found` | wrong ID. Copy it from `stromboli investigations` |
| brief shows no logs or metrics | `catalog validate --check-connectors`, then `connector test <name>` and fix the selector |
| `the OAuth login expired or was revoked` | user runs `stromboli connector login <name>` |
| `decrypt failed: was STROMBOLI_SECRET_KEY changed?` | the server's secret key changed. Restore it, or re-add each connector with `--replace` |
| streaming stops early | a proxy cuts long responses. `stromboli show <id>` still has everything |

`--debug` (or `STROMBOLI_DEBUG=1`) prints the CLI's requests on stderr. Server-side problems show up in the server logs, which the team reads on their host (`fly logs`, `railway logs`, `docker compose logs stromboli`).
