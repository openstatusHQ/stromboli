# How to script the CLI

The CLI follows [clig.dev](https://clig.dev): data goes to stdout, messages to stderr, and every command has `--help` with examples. That makes it easy to use in scripts and CI.

## Authenticate without a terminal

```sh
export STROMBOLI_URL=https://stromboli.example.com
export STROMBOLI_API_TOKEN=...          # or: --token-file /run/secrets/stromboli-token
```

## Never prompt

In CI, pass `--no-input` so a command fails instead of waiting for input. Commands that ask for confirmation then need `--yes`:

| Command | Asks for | Non-interactive |
|---|---|---|
| `connector rm` | confirmation | `--yes` |
| `catalog import --prune` | confirmation, when it would delete something | `--yes` |
| `actions approve` | confirmation, and your name | `--yes`, `--as <name>` or `STROMBOLI_USER` |
| `actions reject` | your name | `--as <name>` or `STROMBOLI_USER` |
| `connector add` with a key and no value | the secret | `key=@file` or `key=-` (stdin) |

Without a terminal, commands behave as with `--no-input`.

## Pass secrets safely

```sh
stromboli connector add github github token=@/run/secrets/github-token
echo "$AXIOM_TOKEN" | stromboli connector add axiom axiom token=-
```

Only one value can be read from stdin per command. A secret typed inline (`token=ghp_...`) works but prints a warning, since it ends up in your shell history.

## Read JSON

`--json` prints the API response as JSON on stdout:

```sh
stromboli catalog list --json | jq -r '.services[].name'
stromboli actions list --status proposed --json | jq -r '.actions[].id'
```

`investigate --json` and `show --follow --json` print one event per line, a step or the finished investigation:

```sh
stromboli investigate api --json | jq -c 'select(.finished) | .finished.report'
```

## Start an investigation and come back to it

```sh
id=$(stromboli investigate api --description "deploy check" --no-watch)
stromboli show "$id" --follow
```

## Check exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | error (including `catalog validate` with errors, or an action that failed) |
| `2` | bad usage: unknown flag, missing argument |
| `130` | interrupted (Ctrl-C) |

## Quiet it down

`-q` silences informational messages; errors, warnings and requested data are still printed. `--debug` (or `STROMBOLI_DEBUG=1`) prints debug logs on stderr.

## Shell completion

```sh
stromboli completion bash > /etc/bash_completion.d/stromboli   # or zsh, fish, powershell
```

## See also

- [CLI reference](../reference/cli.md)
