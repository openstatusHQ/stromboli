# 0003. The service catalog is owned by stromboli

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

The agent's value comes from knowing every service: owners, dependencies, where its logs and metrics live, how it deploys, its runbooks. Knowledge graphs are the core asset of every AI SRE product we studied (Rootly Atlas, incident.io Nexus, Uber's Context Graph). Some teams want that catalog reviewed in git; others just want to edit it.

## Decision

- The catalog is stored in stromboli's database and edited through the API (later the UI). It does not depend on any other repository.
- **YAML is the import/export format.** Teams who want GitOps keep `services.yaml` in git and sync it from CI with `stromboli catalog import services.yaml --runbooks runbooks/ --prune --yes`.
- Validation runs on every write and import: schema, unknown dependencies, cycles, missing runbooks.
- The catalog is loaded as a graph with upstream, downstream and blast radius queries.
- Alerts map to services through `monitors:` (openstatus monitor IDs) and alert labels (`service`, `app`).
- The catalog is declared, not auto-discovered. `stromboli catalog discover <connector>` proposes entries from what a log or metric connector sees, so users only fill in owners and dependencies.

## Consequences

- Onboarding is quick (discover, review, import) without the cost of full auto-discovery.
- The database is the source of truth; a team using GitOps must treat its YAML as authoritative and run `--prune` from CI.
- Auto-discovery (Kubernetes, OTel service graph) remains future work.
