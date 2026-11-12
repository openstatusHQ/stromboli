# CLI reference

`stromboli` is one binary: `serve` runs the server, `migrate` applies migrations, and every other command is a client of a running server's [API](api.md). Run any command with `--help` for its description and examples.

```text
stromboli [global flags] <command> [args] [flags]
```

## Global flags

| Flag | Default | |
|---|---|---|
| `--url <url>` | `$STROMBOLI_URL`, else `http://localhost:8080` | server URL |
| `--token-file <path>` | | read the API token from this file instead of `STROMBOLI_API_TOKEN` |
| `--json` | `false` | print machine-readable JSON on stdout |
| `-q`, `--quiet` | `false` | only print errors, warnings and requested data |
| `--no-input` | `false` | never prompt; fail instead of asking for confirmation or secrets |
| `--debug` | `$STROMBOLI_DEBUG` | print debug logs on stderr |
| `-v`, `--version` | | print the version |
| `-h`, `--help` | | print help |

## Environment

| Variable | |
|---|---|
| `STROMBOLI_URL` | server URL (default `http://localhost:8080`) |
| `STROMBOLI_API_TOKEN` | API token sent as `Authorization: Bearer <token>` |
| `STROMBOLI_USER` | your name, recorded when you approve or reject an action (default of `--as`) |
| `STROMBOLI_DEBUG` | any value except empty, `0`, `false`, `no`, `off` enables debug logs |

`serve` and `migrate` read the [server configuration](configuration.md) instead.

## Output conventions

- Data goes to stdout; messages, warnings and errors to stderr.
- With `--json`, stdout holds the [API](api.md) response as JSON, with the proto field names (`snake_case`) and every field present, even when empty. `investigate` and `show --follow` print one `WatchInvestigationResponse` per line: `{"step": {...}}` or `{"finished": {...}}`.
- Prompts (secrets, confirmations, your name) only appear when stdin and stderr are terminals and `--no-input` is not set. Otherwise the command fails and says which flag to pass.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | error |
| `2` | usage error (unknown flag or command, wrong arguments); followed by `Run '<command> --help' for usage.` |
| `130` | interrupted |

## Commands

| Group | Commands |
|---|---|
| Setup | [`connector`](#connector), [`catalog`](#catalog) |
| Investigations | [`brief`](#brief), [`investigate`](#investigate), [`investigations`](#investigations), [`show`](#show), [`actions`](#actions) |
| Server | [`serve`](#serve), [`migrate`](#migrate) |
| Integrations | [`mcp`](#mcp) |
| Other | `completion bash\|zsh\|fish\|powershell`, `help` |

---

### connector

Alias: `connectors`. Manage connectors to logs, metrics, deploys and machines. Secrets are encrypted on the server and never printed back. Kinds and keys: [Connectors](connectors.md).

#### connector add

```text
stromboli connector add <name> <kind> [key=value]... [--replace]
```

Saves a connector, then tests it (as `connector test`).

| Argument form | Value |
|---|---|
| `key=value` | the value as given; a secret key given this way prints a warning |
| `key` | prompted, input hidden (fails without a terminal) |
| `key=@path` | read from a file |
| `key=-` | read from stdin (one key per command) |

Values are trimmed. An empty secret is an error, as is a key given twice.

| Flag | |
|---|---|
| `--replace` | overwrite an existing connector with the same name. Without it, an existing name fails with a hint |

For a kind that logs in with OAuth (`vercel`), opens the provider's consent page in a browser first and waits for its redirect to `127.0.0.1`. When the browser runs on another machine, paste the address it was redirected to. Needs a terminal.

After a log or metrics connector is saved, suggests `stromboli catalog discover <name>`.

#### connector login

```text
stromboli connector login <name>
```

Logs an OAuth connector in again, for example after `the OAuth login expired or was revoked`. Keeps its other settings, saves the new login, then tests the connector.

#### connector list

Alias: `ls`. Prints `NAME`, `KIND`, `CONFIG`; secret values are shown as `••••`.

#### connector test

```text
stromboli connector test <name>
```

Asks the server to reach the connector. Fails (exit 1) with the server's message if it cannot.

#### connector rm

Aliases: `remove`, `delete`. Deletes a connector and its stored secrets. Asks for confirmation.

| Flag | |
|---|---|
| `-y`, `--yes` | do not ask |

---

### catalog

Manage the [service catalog](catalog.md).

#### catalog list

Alias: `ls`. Prints `NAME`, `TIER`, `OWNERS`, `DEPENDS ON`, `SOURCES` (e.g. `logs=loki metrics=fly deploys=github`).

#### catalog import

```text
stromboli catalog import <services.yaml | -> [flags]
```

Creates or updates the services of a YAML file (`-` reads stdin) and uploads runbooks. Validation issues are printed on stderr as `error|warning: <service>.<field>: <message>`; errors abort the import.

| Flag | |
|---|---|
| `-r`, `--runbooks <dir>` | upload every `*.md` file in the directory (recursively), referenced as `runbooks/<relative path>` |
| `--prune` | delete services and runbooks that are not in the file. Lists what it would delete and asks for confirmation |
| `-n`, `--dry-run` | validate only, change nothing |
| `-y`, `--yes` | do not ask before `--prune` deletes anything |

Prints `Imported N services and N runbooks; deleted N services.`

#### catalog export

Prints the catalog as YAML on stdout, sorted by service name.

| Flag | |
|---|---|
| `-r`, `--runbooks <dir>` | also write the runbooks into this directory, in the layout `import --runbooks` expects |

#### catalog validate

Checks the stored catalog: schema, unknown dependencies, cycles, missing runbooks, connector kinds. Exits 1 if there are errors; warnings alone do not fail.

| Flag | |
|---|---|
| `--check-connectors` | also run a test query for each service's log and metrics selector |

#### catalog discover

```text
stromboli catalog discover <log-or-metrics-connector>
```

Prints one YAML catalog entry per service the connector sees that is not in the catalog yet, with its selector filled in. For a metrics connector, also existing services without metrics, as their full entry with `metrics` filled in. What each kind proposes: [Connectors](connectors.md).

---

### brief

```text
stromboli brief <service> [-s, --since <duration>]
```

Prints the Markdown [brief](agent.md#the-brief) of a service, built without the LLM.

| Flag | Default | |
|---|---|---|
| `-s`, `--since` | `1h` | look-back window, e.g. `30m` or `2h` (max `168h`) |

### investigate

```text
stromboli investigate <service> [flags]
```

Starts an investigation (source `manual`) and streams its steps until it finishes. Ctrl-C stops watching; the investigation keeps running on the server.

| Flag | |
|---|---|
| `-t`, `--title <text>` | investigation title |
| `-d`, `--description <text>` | what you observed; added to the alert payload |
| `--no-watch` | print only the investigation ID on stdout and return |

Stream format: `· built brief for <service>`, `→ <tool> <input> (<duration>)` per tool call (`✗` on error), model text prefixed with `·`, the mitigation block, then the mitigation, the report and token counts.

### investigations

Lists recent investigations: `ID`, `CREATED`, `STATUS` (`running`, `completed`, `failed`), `SERVICES`, `TITLE`.

| Flag | Default | |
|---|---|---|
| `--limit <n>` | `20` | maximum number to list |

### show

```text
stromboli show <investigation-id> [-f, --follow]
```

Prints an investigation's header (title, source, services, time, status), every step, then its mitigation and report.

| Flag | |
|---|---|
| `-f`, `--follow` | if it is running, keep streaming new steps until it finishes. With `--json`, replays every step as a stream |

---

### actions

Alias: `action`. With no subcommand, `stromboli actions [flags]` is `actions list`. See [Machine actions](machine-actions.md).

#### actions list

Prints `ID`, `CREATED`, `STATUS`, `SERVICE`, `ACTION`, `BY`.

| Flag | Default | |
|---|---|---|
| `--status <status>` | | `proposed`, `running`, `succeeded`, `failed`, `rejected`, `expired`, `superseded` |
| `--investigation <id>` | | only this investigation's actions |
| `--limit <n>` | `20` | maximum number to list |

#### actions show

```text
stromboli actions show <action-id>
```

Prints the action, its status, the approve command and expiry if proposed, who decided and when, the note, the result and the error. Exits 1 if the action failed.

#### actions approve

```text
stromboli actions approve <action-id> [flags]
```

Fails if the action is not `proposed`. Asks for confirmation, then runs the action synchronously and prints its outcome. Exits 1 if it failed.

| Flag | |
|---|---|
| `--as <name>` | your name, recorded as the approver. Default `$STROMBOLI_USER`; asked interactively if neither is set |
| `--note <text>` | note recorded with the approval |
| `-y`, `--yes` | do not ask for confirmation |

#### actions reject

```text
stromboli actions reject <action-id> [flags]
```

| Flag | |
|---|---|
| `--as <name>` | your name, recorded with the decision. Default `$STROMBOLI_USER`; asked interactively if neither is set |
| `--note <text>` | reason recorded with the rejection |

---

### serve

Runs the server: the API, webhooks, the agent and, if configured, health checks and the MCP endpoint. Reads the [configuration](configuration.md) from the environment and applies migrations on startup. Stops on SIGINT or SIGTERM, waiting up to 15 seconds for requests and for running investigations to stop.

### migrate

Applies database migrations to `STROMBOLI_DB_URL` and exits. Only `STROMBOLI_DB_URL` is read.

### mcp

Serves stromboli's [MCP tools](mcp-tools.md) over stdio, calling the server at `--url` with `STROMBOLI_API_TOKEN`. stdout carries the protocol; logs go to stderr.
