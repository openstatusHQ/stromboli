# 0006. Suggest, don't execute

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

An agent that can change production can make an incident worse, and teams will not grant it credentials they cannot trust. OpenAI's SevBot and Google's ProdAgent stay read-only by default; actions go through allow-listed tools with policy checks and an audit trail.

## Decision

- stromboli **suggests and never acts on its own**. Every connector uses read-only credentials, and every tool exposed to the model is read-only.
- Mitigations are text: the exact command, how to verify it, how to undo it and its risk. A human applies them.
- The only exception is opt-in, allow-listed machine actions that a named human approves ([0013](0013-approved-fly-machine-actions.md)).
- Redaction (tokens, passwords, emails) runs before anything reaches the model.
- Every brief, tool call and model turn is stored, so an investigation can be audited and replayed.
- stromboli does not write code or open PRs. It hands context to an engineer or a coding agent.

## Consequences

- Users can connect stromboli with the same read-only tokens they would give a new on-call engineer.
- Time to mitigation still depends on a human applying the fix; [0013](0013-approved-fly-machine-actions.md) narrows that gap without removing the human.
