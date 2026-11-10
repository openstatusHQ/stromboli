# Mitigate first, investigate second

When an alert fires, two clocks start: how long users are hurt, and how long until someone understands why. stromboli optimises the first. It publishes a mitigation within a few tool calls, then looks for the root cause ([ADR 0009](../adr/0009-mitigate-first-investigate-second.md)).

## Why not root cause first

The obvious design for an AI SRE is to investigate until it finds the cause, then say what to do. That makes the on-call engineer wait for the slowest, least certain step before getting anything actionable. A thorough investigation takes minutes; some never reach a confident answer.

Experienced incident responders work the other way around, as [Google's SRE practice](https://cloud.google.com/blog/topics/developers-practitioners/how-google-sres-use-gemini-cli-to-solve-real-world-outages) describes: stop the bleeding first. Most outages can be mitigated without knowing their cause. If errors started right after a deploy, roll it back; whether the bug is in the new query or the new config can wait.

So an investigation has two phases. Triage ends as soon as the model submits a mitigation. That is when stromboli posts it to Slack, with a thread reply so on-call gets a notification, and records `mitigated_at`, the number that measures this design. Then the root-cause investigation starts.

## Why a closed set

The mitigation is one of a fixed list of *generic mitigations*: `rollback`, `restart`, `scale_up`, `drain`, `disable_feature`, `block_traffic`, `failover`, `degrade`, or `none`. Each stops impact without knowing the root cause.

A closed set does several things a free-text suggestion would not:

- It is **easy to classify from symptoms**. The system prompt maps typical signals to kinds: errors right after a deploy → rollback; one bad region or instance → drain; saturation under load → scale up; leaks and OOM kills → restart. A fast, cheap model can do that well.
- It is **easy to act on**. Each mitigation has the same parts: target, exact command, how to verify, how to undo, and a risk of `safe`, `reversible` or `destructive`. An engineer can judge it in seconds.
- It allows **`none`**. When nothing is safe, or there is no evidence of user impact, saying so is a valid answer and better than inventing an action.
- It maps to **executable actions** where that is safe: on Fly.io, a restart, clone or cordon can become a [machine action](../reference/machine-actions.md) a human approves in one step.

## Why a separate, faster model

Because the phases have different goals, they can use different models ([ADR 0016](../adr/0016-multi-provider-llm-per-task-models.md)). Triage wants speed: a small model with low effort usually classifies symptoms well. The root cause wants depth: a larger model with higher effort. The investigate model takes over the *same conversation*, so it sees everything triage saw, at the cost of one prompt-cache reset at the switch.

## Keeping the model honest

Two rules keep the order:

- After `STROMBOLI_MITIGATION_TOOL_CALLS` tool calls (6 by default) without a mitigation, the model is told users may still be affected and to commit now.
- A report submitted without a mitigation is refused, and the model is sent back to phase 1.

And one rule keeps it from being stubborn: during the investigation, if the evidence shows the mitigation won't help (restarting cannot fix a bad config push; the problem is in a dependency), the model submits a better one. It is republished, and any action still waiting for approval is superseded. The final report says whether the first mitigation was `confirmed` or what to do instead.

## The trade-off

Early mitigations are sometimes wrong, or more drastic than needed. That is why they are suggestions with an undo, a risk level and a confidence, and why anything stromboli can run itself needs a human's approval ([Safety model](safety-model.md)). A good-enough mitigation now, judged by a human, beats a perfect one later.
