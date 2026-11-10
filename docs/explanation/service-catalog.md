# The service catalog

An alert says "api is failing". To do anything useful, stromboli has to know what `api` is: where its logs and metrics live, what it calls and what calls it, how it is deployed, who owns it, and what the runbook says. That is the service catalog.

## What it is used for

- **Matching alerts.** Alerts are mapped to a service by openstatus monitor ID (`monitors`) or by a label naming the service. Without a match, there is nothing to investigate.
- **Finding the data.** Each service names its connectors and selectors: `logs`, `metrics`, `deploys`. The agent's tools take a service name, not a query, and the catalog fills in the rest. The model never needs to know your Loki labels.
- **Following dependencies.** `depends_on` builds a graph. The brief shows what a service calls (downstream), what calls it (upstream) and its blast radius (everything that transitively depends on it), and lists deploys of its dependencies too. Many incidents are a dependency's fault; the graph tells the agent where to look next.
- **Knowledge.** Runbooks are stored with the catalog and listed in the brief, and the system prompt tells the model to prefer a runbook's mitigation. Past investigations are searched by service.
- **Permissions.** For Fly.io, the catalog is where machine actions are allowed, per service, with the connector that may run them.

## Why stromboli owns it

Many teams already have a catalog of some kind: Backstage, a wiki page, labels on Kubernetes objects. stromboli still keeps its own, in its database, edited through its API ([ADR 0003](../adr/0003-service-catalog-owned-by-stromboli.md)).

The alternatives were worse for an incident tool:

- **Depending on an external catalog** would make stromboli only as available as that system, and tie it to one product. Most catalogs also don't carry what stromboli needs: a log selector, a metrics selector, a deploy workflow.
- **Auto-discovering everything** would be convenient but wrong in the details that matter most: ownership, dependencies and runbooks can't be read reliably from telemetry.

So the catalog is **declared, with help**. `catalog discover` proposes one entry per service a log or metrics connector can see, with its selector already filled in, and a human adds owners, dependencies and runbooks.

## Why YAML is only the exchange format

The catalog lives in Postgres, but YAML is how it goes in and out. That gives two workflows with one source of truth:

- Teams that like GitOps keep `services.yaml` and the runbooks in git, review changes in pull requests, and sync from CI with `catalog import --prune`, which makes the file authoritative.
- Teams that don't can edit through the API (and later a UI) without a repository.

Validation runs on every write: names, unknown dependencies and cycles, missing runbooks, and connectors of the wrong kind. Problems that would make data unreachable or actions unsafe are errors and block the write; gaps that only make investigations poorer (no owners, no log source, an external dependency not in the catalog) are warnings.

## Why it is small

The catalog has few fields on purpose. Each one is there because an investigation uses it. Anything else (cost centres, SLAs, links to dashboards) belongs in the tools that already track it.
