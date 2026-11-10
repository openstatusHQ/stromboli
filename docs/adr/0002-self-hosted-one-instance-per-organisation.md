# 0002. Self-hosted, one instance per organisation

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

An AI SRE agent needs credentials to logs, metrics and deploys, and sees production data during every incident. Trust in a vendor holding that data is the main concern teams raise about hosted AI SRE products ([The Pragmatic Engineer: AI SRE](https://newsletter.pragmaticengineer.com/p/ai-sre)). Our audience already self-hosts its tooling.

## Decision

stromboli is self-hosted, in the Metabase style:

- `docker compose up` (stromboli + Postgres) gives a working instance. Teams with Postgres run the image alone with `STROMBOLI_DB_URL`.
- **One instance is one organisation.** There is no multi-tenancy in the schema; isolation comes from separate deployments.
- Teams bring their own LLM API key and their own log, metric and deploy sources.
- Connector credentials are set through the API and encrypted at rest with `STROMBOLI_SECRET_KEY` (AES-256-GCM). Everything can also be set through env vars for infrastructure-as-code users.

## Consequences

- Customer data never leaves their infrastructure, apart from LLM calls.
- The data model stays simple: no tenant IDs, no row-level isolation.
- We cannot offer a shared hosted instance without revisiting this decision.
- Upgrades are run by users, so the API must stay backwards compatible ([0005](0005-go-binary-api-first-connectrpc.md)).
