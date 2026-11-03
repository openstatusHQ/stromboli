# 0010. Investigation flow: deterministic brief, then a single agent

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

Most of the value of an AI SRE is in the investigation phase: collecting context faster than a human can. Much of that context is the same for every incident and does not need a model.

## Decision

1. **Trigger.** An openstatus, Alertmanager, Grafana or generic webhook, or `opensre investigate`, creates an investigation for a catalog service. Repeats within `OPENSRE_DEDUPE_WINDOW` are dropped.
2. **Brief, with no LLM.** Code collects the service card, dependencies and blast radius, log patterns and metrics against baseline, recent deploys of the service and its dependencies, runbooks and similar past investigations. `opensre brief` prints it on its own, and it must be useful to a human without any model.
3. **One agent with read-only tools** then mitigates and investigates ([0009](0009-mitigate-first-investigate-second.md)), within `OPENSRE_MAX_TOOL_CALLS` and `OPENSRE_INVESTIGATION_TIMEOUT`.
4. **Report.** A fixed shape: likely cause, confidence, evidence, ruled out, mitigation, long-term fix, next steps, sent to Slack and the API.

Slack is the primary output: one thread per incident, updated in place rather than spammed, quiet unless there is something new.

## Consequences

- The brief is cheap, testable and reusable outside investigations.
- A single agent can anchor on its first guess in long incidents; [0017](0017-multi-role-investigation.md) proposes separate roles to counter that.
