# 0015. The CLI follows clig.dev

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

Until the web UI exists, the CLI is how users set up and operate stromboli, by hand and from scripts and CI.

## Decision

The CLI follows the [Command Line Interface Guidelines](https://clig.dev):

- Data goes to stdout and messages to stderr. `--json` prints JSON (one event per line for `investigate`), `-q` silences messages.
- Exit codes: `0` success, `1` error, `2` bad usage, `130` interrupted.
- Secrets stay off the command line: a bare `token` prompts with hidden input, `token=@file` reads a file, `token=-` reads stdin.
- Destructive commands (`connector rm`, `actions approve`, `catalog import --prune` when it deletes) ask first; without a terminal or with `--no-input` they fail unless `--yes` is passed.
- Every command has `--help` with examples, and shell completions are generated.

## Consequences

- The same commands work interactively and in CI.
- New commands must follow these conventions.
