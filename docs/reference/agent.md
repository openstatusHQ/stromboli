# Agent reference

An investigation is a deterministic brief, then one LLM conversation with read-only tools, in two phases: **triage** until `submit_mitigation` succeeds, then **investigate** until `submit_report`. The code is in [`internal/agent`](../../internal/agent).

## The brief

Built without the LLM, for every service of the investigation, and also by `stromboli brief`, `GetBrief` and the `get_brief` MCP tool. Each section degrades to a note when its source is missing or fails.

**Window.** For an investigation, from 30 minutes before the alert's time until now, bounded to the last 24 hours. For `brief --since <d>`, the last `<d>`. The **baseline** is the window of the same length right before it.

| Section | Content |
|---|---|
| Service | the catalog entry, as YAML |
| Dependencies | depends on (downstream), called by (upstream), blast radius |
| Logs | total and error-like line counts against the baseline, and the top 10 error patterns with incident and baseline counts, `NEW` when absent from the baseline, first-seen time and a redacted sample |
| Metrics | presets marked *brief* of the service's metrics connector, against the baseline |
| Recent changes | deploys (or commits) of the service and its direct dependencies, from 2 hours before the window |
| Fly.io | for Fly services: the app's machines (ID, region, state, volumes), whether stromboli can run machine actions, and the Fly command with its undo for each mitigation |
| Runbooks | the service's runbooks |
| Similar past investigations | the 3 most recent finished investigations of the service, with their reported cause |

## Conversation

The first message holds the investigation title, source, services, current time, the alert payload as JSON, and the briefs. The system prompt and tool definitions are static, so providers that cache prompts reuse them across investigations.

| Rule | Value |
|---|---|
| Max output tokens per turn | 16 000 |
| Tool calls before the agent is told to submit a mitigation now | `STROMBOLI_MITIGATION_TOOL_CALLS` (6) |
| Tool calls before the agent is told to stop and submit | `STROMBOLI_MAX_TOOL_CALLS` (30) |
| Tool calls after which the investigation fails | `STROMBOLI_MAX_TOOL_CALLS` + 10 |
| Wall-clock limit | `STROMBOLI_INVESTIGATION_TIMEOUT` (8m) |
| Timeout per tool call | 60 s |
| Max tool result | 24 KiB, then truncated with `… (truncated: narrow the query)` |

Tools called in the same turn run in parallel. A `submit_report` before any accepted mitigation is rejected and the agent is sent back to `submit_mitigation`. If the model answers twice in prose without calling a tool, its text becomes the report summary (cause `unknown`, confidence `low`). A refusal or hitting the output token limit fails the investigation.

## Investigation tools

All read-only. `since_minutes` and `until_minutes` count back from the investigation's start.

| Tool | Arguments | Returns |
|---|---|---|
| `list_services` | | every service with its dependencies, tier, and whether it has logs and metrics |
| `get_service` | `service` | the catalog entry, upstream, downstream and blast radius |
| `log_patterns` | `service`, `filter` (RE2; default error-like, `.` for all), `since_minutes` (30, max 1440), `until_minutes` (0) | log patterns in the window compared with the baseline (top 15) |
| `query_logs` | `service`, `filter`, `since_minutes` (30, max 1440), `until_minutes` (0), `limit` (30, max 100) | raw lines, newest first, each truncated to 500 characters and redacted |
| `log_timeline` | `service`, `filter` (default error-like), `since_minutes` (60, max 1440), `buckets` (12, max 24) | matching line counts per time bucket |
| `get_metrics` | `service`, `metric` (a preset) or `query` (PromQL with `$selector`), `since_minutes` (60, max 1440), `until_minutes` (0) | series with min, max, avg, last, baseline average and 12 points, at most 10 series |
| `list_metrics` | `service` | the connector's presets and the raw metric names with series for the service in the last hour |
| `recent_deploys` | `service`, `since_minutes` (240, max 10080) | deploys, or commits when no workflow or environment is configured |
| `get_commit` | `service`, `sha` | message, author and changed files with truncated diffs |
| `read_runbook` | `path` | the runbook's Markdown |
| `search_past_investigations` | `service`, `query` (in the title or report) | up to 5 finished investigations, newest first, with their cause |

Log filters are case-insensitive. Tool output (log lines, patterns, commit diffs) is redacted before the model sees it: see [Redaction](#redaction).

## Submit tools

### submit_mitigation

Phase 1. Can be called again to revise the mitigation; each call republishes it (and supersedes a pending machine action). Fields, all required except `machine_action`:

| Field | |
|---|---|
| `kind` | `rollback`, `restart`, `scale_up`, `drain`, `disable_feature`, `block_traffic`, `failover`, `degrade`, `none` |
| `target` | service, deploy SHA, region, instance, flag or dependency |
| `action` | the exact command(s) a human runs |
| `rationale` | which symptoms point to it |
| `verify` | how to tell within minutes that it worked |
| `undo` | how to revert it |
| `risk` | `safe`, `reversible`, `destructive` |
| `confidence` | `low`, `medium`, `high` |
| `impact` | current user impact |
| `machine_action` | `{service, action, machine_ids, region}`: only when the brief says stromboli can run actions for the service. See [Machine actions](machine-actions.md) |

When `machine_action` is missing but `action` holds a `fly machine restart|clone|cordon|uncordon|stop|start` command stromboli could run, it is filled in from the command. A `machine_action` that fails the [guardrails](machine-actions.md#guardrails) is dropped: the mitigation is published as a suggestion and the agent is told why.

On success: the mitigation is stored, `mitigated_at` is set (on the first one), it is posted to Slack, and the investigate model takes over.

### submit_report

Phase 2. Ends the investigation. Fields, all required:

| Field | |
|---|---|
| `summary` | what is happening, for someone joining the incident |
| `likely_cause` | one or two sentences, or `unknown` |
| `confidence` | `low`, `medium`, `high` |
| `evidence` | at most 4 observations |
| `ruled_out` | at most 4 rejected hypotheses, with the reason |
| `suggested_mitigation` | `confirmed`, what to do instead and why, or `none` |
| `fix` | the long-term fix, or `unknown` |
| `next_steps` | at most 3 things a human should check |

## Mitigation kinds

| Kind | When the agent picks it |
|---|---|
| `rollback` | errors started right after a deploy of this service |
| `restart` | stuck, leaking or wedged processes: memory climbing, OOM kills; a full root filesystem |
| `scale_up` | saturation under load: CPU near 100%, throttling, concurrency at its limit; legitimate memory use; a full volume (extend it) |
| `drain` | one region, zone or instance bad while others are healthy |
| `disable_feature` | a flag-guarded change |
| `block_traffic` | abusive or malformed traffic |
| `failover` | a failing dependency with a standby |
| `degrade` | shed non-essential work to keep the core working |
| `none` | nothing is safe, or no evidence of user impact |

## Redaction

Applied before data reaches the model or is stored: raw log lines, log patterns and their samples, deploy titles, commit messages and diffs, and metric query errors.

| Matches | Replaced by |
|---|---|
| `Bearer …`, `Basic …` credentials | `Bearer [REDACTED]` |
| JWTs | `[REDACTED_JWT]` |
| AWS access key IDs (`AKIA…`, `ASIA…`) | `[REDACTED_AWS_KEY]` |
| GitHub tokens (`ghp_…`, `github_pat_…`) | `[REDACTED_GITHUB_TOKEN]` |
| Stripe keys (`sk_live_…`) | `[REDACTED_STRIPE_KEY]` |
| `sk-…` API keys | `[REDACTED_API_KEY]` |
| Slack tokens (`xox…`) | `[REDACTED_SLACK_TOKEN]` |
| values of keys named `password`, `secret`, `token`, `api_key`, `access_key`, `private_key`, `client_secret`, `authorization`, `cookie`… | `[REDACTED]` |
| credentials in URLs (`scheme://user:pass@`) | `scheme://[REDACTED]@` |
| email addresses | `[REDACTED_EMAIL]` |

## Audit trail

Every brief, tool call (input, output, duration, error), model turn (task, model, stop reason, input, cache read, cache write and output tokens), mitigation, report and error is stored as a [step](api.md#step), in order. Token totals are added to the investigation after each turn.
