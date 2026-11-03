# 0019. Evaluation by replaying recorded incidents

- **Status:** Proposed
- **Date:** 2026-11-02

## Context

Measuring accuracy is the hard part of an AI SRE. Without numbers, prompt, model and role changes ([0016](0016-multi-provider-llm-per-task-models.md), [0017](0017-multi-role-investigation.md)) are guesses.

## Decision

- Snapshot connector responses for real past incidents into `eval/cases/<id>/`, with the known root cause. The stored evidence ([0007](0007-query-logs-on-demand.md)) is the starting point.
- `opensre eval` replays each case with connectors stubbed by the fixtures, and scores:
  - whether the root cause is the top-1 or in the top-3 hypotheses, judged by an LLM against the known answer and spot-checked by humans
  - time to mitigation, time to report, tokens and cost
  - hallucinated evidence: claims not backed by any tool output
- It runs in CI on prompt or model changes, and scores are tracked over time.
- Synthetic incidents (a bad deploy or a killed dependency in staging) grow the set.

## Consequences

- Changes to the agent are judged on a number, starting from a baseline with 10+ cases.
- Fixtures must be redacted before they are committed.
