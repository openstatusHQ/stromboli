# 0004. Postgres as the only storage

- **Status:** Accepted
- **Date:** 2026-11-02

## Context

stromboli stores the catalog, connectors, investigations, their steps and the audit trail. It will need vector search for similar incidents and runbooks, flexible storage for evidence, and safe concurrent writes from parallel sub-investigations. Self-hosters must be able to back it up and run it managed.

## Decision

- Postgres 16+ is the only database, with `pgvector` for embeddings and JSONB for evidence.
- Access goes through `pgx`, queries are generated with `sqlc`, and migrations use `goose`, embedded in the binary and run on startup.
- A store interface keeps the door open for other databases, but no second adapter is planned unless users ask for one.

## Consequences

- One SQL dialect to write and test.
- Self-hosters can use RDS, Cloud SQL, Neon or Supabase.
- There is no embedded, zero-dependency mode (such as SQLite); compose ships Postgres instead.
