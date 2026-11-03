# 0018. Memory notes with a lifecycle

- **Status:** Proposed
- **Date:** 2026-11-02

## Context

Teams carry knowledge no catalog field holds: "checker latency spikes in syd usually mean a Fly region degradation; check Fly status first". Agent memory goes stale, and a stale note confidently misleads.

## Decision

- Notes are short facts scoped to a service, created by a human (Slack `/sre remember …`) or proposed by the agent after a resolved incident, which needs human approval.
- Each note moves through `candidate → active → quarantined`. A note is quarantined when an investigation contradicts it, and tracks `last_useful_at`.
- Only `active` notes go into the brief.
- Similar past investigations and runbooks move from full-text search to `pgvector` embeddings ([0004](0004-postgres-only-storage.md)).

## Consequences

- Knowledge improves with each incident without unreviewed facts reaching the prompt.
- Someone has to review candidate notes. Success target: notes used in at least 30% of investigations.
