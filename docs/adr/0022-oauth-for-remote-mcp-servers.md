# 0022. OAuth for remote MCP servers

- **Status:** Proposed
- **Date:** 2026-11-12

## Context

[0011](0011-mcp-first-connectors.md) has stromboli call a provider through the MCP server it hosts. Until now those servers accepted a token or a service account. Some only accept OAuth: Vercel's (`mcp.vercel.com`) takes no access token, only an OAuth login from a client that publishes a client ID metadata document or registers dynamically, as the MCP authorization spec describes.

stromboli runs as a server with no browser, and each self-hosted instance has its own address, so it cannot receive an OAuth redirect at a fixed URL. Access tokens last hours, so the connector must keep working without a person.

## Decision

- **The CLI logs in, the server refreshes.** For an OAuth kind, `connector add` (and `connector login` later) runs the authorization code flow with PKCE on the user's machine: discovery, client registration, a redirect to `127.0.0.1`, or the redirect address pasted back when the browser runs elsewhere. It sends the server the client ID, token endpoint and refresh token; the refresh token is stored encrypted like any secret. The MCP Go SDK does discovery, registration and the exchange.
- **A login must yield a refresh token**, or it fails: without one the connector would stop within hours.
- **The server keeps one token source per connector**: it refreshes the access token before it expires and saves a rotated refresh token at once. The save is skipped if the connector was logged in again meanwhile (`authorized_at` changed), so an old token never overwrites a new login. The rotating token is left out of the MCP session pool's fingerprint, so a refresh keeps the session; a new login replaces it.
- **Client identity:** a client ID metadata document when one is configured (`client_metadata_url`) and the server supports them, else dynamic registration at login.
- When the refresh token is expired or revoked, calls fail with `run stromboli connector login <name>`.
- The rules of 0011 still apply: an allow-list of tools, and the read-only check for servers that annotate their tools. One exception: a connector may name a tool it only calls read-only although the server does not mark it so, when the write is an argument stromboli never sets. Axiom's `queryDataset` is the case: `saveResults` stores a result snapshot, and stromboli always sends `false`. Such a tool is also retried like a read-only one.
- A connector may offer both: Axiom keeps its REST API with a token (scoped to datasets) and uses its MCP server with OAuth without one.

## Consequences

- Providers whose MCP servers only take OAuth can be connected without a hosted callback or a stromboli-run OAuth app.
- An OAuth login usually grants the user's whole account (Vercel: "the same access as your Vercel user account"), wider than a scoped token. The tool allow-list is what limits stromboli; the stored refresh token is as sensitive as that account.
- Someone must log in again when the provider expires or revokes the refresh token. The connector test and the health-check warning show it.
- Several stromboli replicas sharing a connector could race on a rotation; one instance per organisation ([0002](0002-self-hosted-one-instance-per-organisation.md)) avoids it.
