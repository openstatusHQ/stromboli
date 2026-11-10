# The life of an investigation

```text
alert ──▶ 1. match ──▶ 2. brief ──▶ 3. mitigate ──▶ 4. investigate ──▶ 5. report
          & dedupe     (no LLM)     (triage model,   (investigate model,  (Slack + API)
                                     posted now)      read-only tools)
```

This page follows one alert through stromboli. The exact limits and fields are in the [agent reference](../reference/agent.md) and the [webhooks reference](../reference/webhooks.md).

## 1. Match and dedupe

An alert arrives on a webhook, from a health check, or as a manual request (CLI, API, MCP). Each source has its own payload, so the first job is to normalise it: a title, the services it concerns, a fingerprint, the time the problem was observed, and whether it is a recovery.

**Matching** maps the alert to catalog services: by openstatus monitor ID through the catalog's `monitors`, or by the first label (`service`, `service_name`, `app`, `job`, `container`) that names a catalog service. An alert that matches nothing is ignored. stromboli does not guess: investigating the wrong service wastes tokens and, worse, misleads the on-call engineer.

**Dedupe** handles the fact that alerting tools repeat themselves. A repeat (same fingerprint) is merged into the investigation already running for it. Once that investigation has finished, a repeat within `STROMBOLI_DEDUPE_WINDOW` still reuses its result rather than paying for the same answer twice.

Two events end that reuse, because they mean the next alert is a *new* incident even if it looks the same:

- the alert source reports the problem **resolved** (openstatus `recovered`, Alertmanager `resolved`);
- one of the investigation's **machine actions succeeded**: something was changed, so if the alert fires again, the change did not fix it.

## 2. Brief

Before any model is involved, code collects what an on-call engineer would look at first: the service and its dependencies, log error patterns and metrics compared with the preceding window of the same length, recent deploys of the service and its dependencies, runbooks, similar past investigations and, on Fly.io, the app's machines and the exact commands for each mitigation.

The brief is deterministic for three reasons ([ADR 0010](../adr/0010-investigation-flow.md)):

- It must be **useful without a model**. `stromboli brief` prints it on its own, for free.
- It is **fast and cheap**: it runs in parallel and needs no model turns, so the model starts with the context instead of spending its first tool calls on it.
- It **anchors the model** in facts. The window starts 30 minutes before the problem was observed, and every comparison is against a baseline, so "errors are up" is a number, not an impression.

Every section degrades to a note when its source is missing or fails: a broken connector makes the brief poorer, not absent.

## 3. Mitigate

The model gets the alert and the briefs, and works in two phases with the same conversation. In the first, **triage**, its only job is to propose one generic mitigation (rollback, restart, scale up, drain…) with the exact command, how to verify it, how to undo it and the risk. After a handful of tool calls it is told to commit; a report without a mitigation is refused.

The mitigation is published as soon as it is submitted: stored with its time (`mitigated_at`), posted to Slack with a thread reply so on-call gets a notification, and visible in the API. [Mitigate first, investigate second](mitigate-first.md) explains why.

## 4. Investigate

The second phase can use a different, more thorough model, which takes over the same conversation and so sees everything triage saw ([ADR 0016](../adr/0016-multi-provider-llm-per-task-models.md)). It forms a few hypotheses and checks them with read-only tools: log patterns and timelines, raw lines, metrics, deploys and commit diffs, runbooks, past investigations.

All tool output is aggregated, size-capped and redacted before the model sees it. Tools called in the same turn run in parallel. The model is asked to correlate timing ("a deploy after the errors started did not cause them"), to try to disprove its leading hypothesis, and to revise the mitigation if the evidence shows it won't help. A revision is republished, and supersedes a pending machine action.

The phase is bounded by a tool budget and a wall-clock timeout. When the budget runs out the model is told to report with what it has; "unknown" with low confidence is an acceptable answer, and invented evidence is not.

## 5. Report

The investigation ends with a report in a fixed shape: summary, likely cause, confidence, evidence, what was ruled out, the mitigation in light of the cause, the long-term fix and next steps. The Slack message is edited into the report, with the supporting details in a thread reply so the conclusion stays on top.

The fixed shape matters: an on-call engineer reads it on a phone, mid-incident, and needs to find the same things in the same place every time.

## The audit trail

Every brief, tool call (with its input and output), model turn (with its token usage), mitigation, report and error is stored as an ordered step. That is how `stromboli investigate` and `show --follow` stream progress (they poll the steps), how a human can check where a conclusion came from, and what future evaluation work replays ([ADR 0019](../adr/0019-evaluation-harness.md)).

## What can go wrong

An investigation fails, rather than returning a weak report, when the model refuses, hits its output limit, keeps calling tools well past the budget, or the timeout expires. A failure is still posted to Slack and kept with its steps. If the server stops, running investigations are marked failed on the next start, and you re-run them.
