# 0013. Approved machine actions, Fly.io only

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

Text mitigations ([0006](0006-suggest-dont-execute.md)) leave time to mitigation dependent on a human copying a command. For Fly.io, the Machines API makes a few actions (restart, clone, cordon) precise, reversible and easy to check, which makes them a safe first step towards executing.

## Decision

- Behind `OPENSRE_ACTIONS_ENABLED` (off by default, and requiring `OPENSRE_API_TOKEN`), a service with a `fly_machines` connector and an allow-list lets the agent attach a `machine_action` (`restart`, `clone`, `cordon`, `uncordon`, `stop`, `start`) to its mitigation. The default allow-list is restart, clone, cordon and uncordon; stop and start are opt-in.
- opensre checks the action against the allow-list and the app's current machines (at most 10; machines with a volume are never cloned) and stores it as **proposed**. A refused action is dropped and the mitigation stays text. Revising the mitigation supersedes it.
- **Nothing runs until a named human approves it** before `OPENSRE_ACTION_TTL` (30 minutes). The approval is checked again, then claimed atomically, so an action runs at most once.
- It runs machine by machine. A restart, start or clone waits for the machine to reach `started` and stops at the first failure.
- Every action records who approved or rejected it, when, their note, the result and how to undo it, and is posted in the Slack thread.
- For every Fly service, the brief lists the app's machines and the Fly command, with its undo, for each generic mitigation, so text mitigations name real machine IDs too.

## Consequences

- A safe class of mitigations can be applied in one click without giving the model write access.
- Anyone with the API token can approve, under any name. [0014](0014-slack-approval-buttons.md) adds verified approvers.
- Other runtimes (Kubernetes, ECS) need their own connector and allow-list.
