# Configuration reference

The server (`stromboli serve`) is configured only with environment variables. [`.env.example`](../../.env.example) lists them with comments. Durations use Go syntax (`30s`, `5m`, `1h30m`); booleans accept `true`/`false`, `1`/`0`, `t`/`f`.

The server refuses to start when a value cannot be parsed or a required setting is missing. Settings that are half-configured (Slack buttons without actions, a summary without health checks…) only log a warning.

## Server

| Variable | Default | |
|---|---|---|
| `STROMBOLI_ADDR` | `:8080` | listen address. Plain HTTP/1.1 and unencrypted HTTP/2 (for gRPC clients) |
| `STROMBOLI_DB_URL` | **required** | Postgres URL. Migrations run on startup |
| `STROMBOLI_SECRET_KEY` | **required** | encrypts connector secrets with AES-256-GCM (the key is the SHA-256 of this value). Must never change once connectors exist. Generate with `openssl rand -base64 32` |

## Authentication

| Variable | Default | |
|---|---|---|
| `STROMBOLI_API_TOKEN` | empty = no auth | bearer token for the [API](api.md). Required by `STROMBOLI_ACTIONS_ENABLED` and `STROMBOLI_MCP_ENABLED` |
| `STROMBOLI_WEBHOOK_TOKEN` | `STROMBOLI_API_TOKEN` | token for `/webhooks/*`, sent as `?token=`, `Authorization: Bearer` or `X-Webhook-Secret`. Empty (with no API token) = no auth |

## Models and providers

| Variable | Default | |
|---|---|---|
| `STROMBOLI_MODEL` | `anthropic:claude-opus-5-5` | default model for every task, as `provider:model` |
| `STROMBOLI_EFFORT` | provider default | `low`, `medium`, `high`, `xhigh` or `max` |
| `STROMBOLI_TRIAGE_MODEL` | `STROMBOLI_MODEL` | model until the mitigation is published |
| `STROMBOLI_TRIAGE_EFFORT` | `STROMBOLI_EFFORT` | |
| `STROMBOLI_INVESTIGATE_MODEL` | `STROMBOLI_MODEL` | model for the root cause and the report; takes over the triage conversation |
| `STROMBOLI_INVESTIGATE_EFFORT` | `STROMBOLI_EFFORT` | |

Model ids:

| Id | Provider | Variables |
|---|---|---|
| `anthropic:<model>`, or a bare `claude-…` id | Anthropic Messages API | `ANTHROPIC_API_KEY` |
| `openai:<model>` | OpenAI Chat Completions | `OPENAI_API_KEY`, `OPENAI_BASE_URL` (default `https://api.openai.com/v1`) |
| `google:<model>` | Gemini, OpenAI-compatible endpoint | `GEMINI_API_KEY` |
| `openrouter:<model>` | OpenRouter | `OPENROUTER_API_KEY` |
| `deepseek:<model>` | DeepSeek | `DEEPSEEK_API_KEY`, `DEEPSEEK_BASE_URL` (default `https://api.deepseek.com`) |
| `gateway:<id>`, or any `<vendor>/<model>` id | Vercel AI Gateway | `AI_GATEWAY_API_KEY` or `VERCEL_OIDC_TOKEN`, `AI_GATEWAY_BASE_URL` |
| `ollama:<model>` | Ollama | `OLLAMA_BASE_URL` (default `http://localhost:11434/v1`) |
| `<name>:<model>` | any OpenAI-compatible API | `STROMBOLI_PROVIDER_<NAME>_BASE_URL` (required), `STROMBOLI_PROVIDER_<NAME>_API_KEY`. `<NAME>` is the name upper-cased, `-` as `_` |

If a model cannot be resolved, the server starts with investigations disabled; briefs still work.

Provider behaviour: Anthropic models (direct or through the gateway's Anthropic Messages API) use prompt caching and effort. Other providers use the OpenAI Chat Completions format, where effort is sent as `reasoning_effort`, only when set. Reasoning a provider returns (DeepSeek's `reasoning_content`, the gateway's `reasoning_details`) is sent back to it on later turns. Switching models between tasks resets the prompt cache once.

## Investigations

| Variable | Default | |
|---|---|---|
| `STROMBOLI_MAX_TOOL_CALLS` | `30` | tool calls per investigation. When reached, the agent is told to submit; it fails after 10 more |
| `STROMBOLI_MITIGATION_TOOL_CALLS` | `6` | tool calls after which the agent is told to submit a mitigation now |
| `STROMBOLI_INVESTIGATION_TIMEOUT` | `8m` | wall-clock limit of one investigation |
| `STROMBOLI_DEDUPE_WINDOW` | `30m` | how long after it finished an investigation still absorbs repeats of its alert ([webhooks](webhooks.md#deduplication)) |

## Machine actions

| Variable | Default | |
|---|---|---|
| `STROMBOLI_ACTIONS_ENABLED` | `false` | let investigations propose [machine actions](machine-actions.md) a human can approve. Requires `STROMBOLI_API_TOKEN` |
| `STROMBOLI_ACTION_TTL` | `30m` | how long a proposed action can be approved. Must be positive |

## Health checks

| Variable | Default | |
|---|---|---|
| `STROMBOLI_HEALTHCHECK_INTERVAL` | empty = off | how often to check Fly metrics against thresholds, at least `5m`. `0` disables |
| `STROMBOLI_HEALTHCHECK_SUMMARY` | `false` | post an all-clear to Slack after each run where every service is healthy, and a warning listing the services that could not be checked. Needs Slack and health checks |

## Slack

| Variable | Default | |
|---|---|---|
| `SLACK_BOT_TOKEN` | | bot token (`xoxb-…`) with `chat:write`. Slack is off unless this and `SLACK_CHANNEL` are set |
| `SLACK_CHANNEL` | | channel name (`#incidents`) or ID (`C0123…`) |
| `SLACK_SIGNING_SECRET` | | enables Apply/Reject buttons at `POST /slack/interactions`. Needs Slack and `STROMBOLI_ACTIONS_ENABLED=true` |
| `SLACK_APPROVERS` | empty = anyone in the channel | comma-separated Slack user IDs allowed to click Apply/Reject |

## MCP server

| Variable | Default | |
|---|---|---|
| `STROMBOLI_MCP_ENABLED` | `false` | serve the [MCP tools](mcp-tools.md) over HTTP at `/mcp`. Requires `STROMBOLI_API_TOKEN` |
| `STROMBOLI_MCP_TOKEN` | `STROMBOLI_API_TOKEN` | when set, the only token `/mcp` accepts; it does not reach the rest of the API |

## Connector credentials from the environment

Connectors are configured through the API and stored in Postgres, not in the environment. Two exceptions, read by the server process:

- `cloudwatch` without keys uses the default AWS credential chain (`AWS_ACCESS_KEY_ID`, `AWS_PROFILE`, an IAM role…).
- `gcp_logging` and `gcp_monitoring` without `credentials_json` use Application Default Credentials (`GOOGLE_APPLICATION_CREDENTIALS`, workload identity…).

## Docker Compose only

Read by [`docker-compose.yml`](../../docker-compose.yml), not by stromboli:

| Variable | Default | |
|---|---|---|
| `STROMBOLI_VERSION` | `latest` | image tag of `ghcr.io/openstatushq/stromboli` |
| `STROMBOLI_PORT` | `8080` | host port |
| `POSTGRES_PASSWORD` | `stromboli` | Postgres password; only applied when the volume is created |

## CLI

The client commands read `STROMBOLI_URL`, `STROMBOLI_API_TOKEN`, `STROMBOLI_USER` and `STROMBOLI_DEBUG`: see the [CLI reference](cli.md#environment).
