# 0009. Mitigate first, investigate second

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

During an incident, what matters most is how fast user impact stops, not how fast the bug is understood. Google's SREs mitigate with generic actions before they know the root cause ([How Google SREs use Gemini CLI](https://cloud.google.com/blog/topics/developers-practitioners/how-google-sres-use-gemini-cli-to-solve-real-world-outages)). An agent that only reports after a full root-cause analysis makes on-call wait for minutes.

## Decision

An investigation has two phases:

1. **Triage.** From the brief and a few tool calls, the model picks one generic mitigation from a closed set: `rollback`, `restart`, `scale_up`, `drain`, `disable_feature`, `block_traffic`, `failover`, `degrade` or `none`. It gives the target, the exact command, how to verify it, how to undo it and the risk (`safe`, `reversible`, `destructive`). The mitigation is published right away to the API, Slack (with a thread reply so on-call is notified) and the audit trail, and its time is stored as `mitigated_at`.
2. **Investigate.** With a mitigation in hand, the model looks for the root cause. If the evidence shows the mitigation will not help, it revises it.

After `STROMBOLI_MITIGATION_TOOL_CALLS` tool calls the model is told to commit to a mitigation, and a report submitted without one is rejected.

## Consequences

- Time to mitigation is measurable per investigation.
- A closed set keeps mitigations comparable and makes runtime-specific commands possible ([0013](0013-approved-fly-machine-actions.md)), at the cost of not covering every fix.
- Each phase can use its own model ([0016](0016-multi-provider-llm-per-task-models.md)).
- Next: a deterministic fast path, with no LLM, for obvious cases such as errors that start right after a deploy → rollback.
