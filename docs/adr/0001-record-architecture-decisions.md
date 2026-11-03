# 0001. Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

The design of opensre lived in a single `PLAN.md` that mixed decisions, a roadmap, status notes and open questions. As the code moved past the plan, the file needed a status banner listing where reality differed, and it was hard to tell which parts were still true and why a choice had been made.

## Decision

We record each significant decision as an Architecture Decision Record in `docs/adr/`, following [Michael Nygard's format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions): context, decision, consequences.

- Files are numbered and never renumbered: `NNNN-short-title.md`, starting from [`template.md`](template.md).
- An ADR is **Proposed** while under discussion and **Accepted** once we commit to it.
- An accepted ADR is not rewritten when we change our mind. A new ADR supersedes it, and the old one's status links to the new one.
- Every ADR is listed in [`README.md`](README.md).

`PLAN.md` is removed. Its decisions are now ADRs 0002 to 0016; the roadmap items it described are Proposed ADRs.

## Consequences

- The reason behind a choice stays next to the code, and its history is in git.
- Writing an ADR is a small extra step in a PR that changes architecture.
- There is no single "plan" document anymore: the index in `README.md` is the overview.
