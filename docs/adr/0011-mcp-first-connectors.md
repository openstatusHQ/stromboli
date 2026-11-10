# 0011. MCP first, else the provider's API

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

Providers increasingly host MCP servers for their APIs. Using them means less client code to maintain, but an MCP server can expose destructive tools, and some providers only offer MCP as a local process that wraps their CLI.

## Decision

- stromboli calls a provider through **the MCP server the provider hosts**, when there is one (Google Cloud Logging and Monitoring, GitHub).
- Otherwise it calls **the provider's API directly** (Fly.io Machines API, Loki and Prometheus HTTP APIs, the AWS SDK, Axiom REST). It never adds a CLI or local process just to get MCP; for example, `flyctl mcp server` serves destroy, exec and secrets commands without marking any read-only.
- stromboli is the MCP client and calls tools from code. The model never talks to these servers directly; it gets compact evidence ([0007](0007-query-logs-on-demand.md)).
- Each connector names the few tools it may call. Any other tool is refused.
- For servers that annotate their tools, connecting fails unless every allowed tool is marked `readOnlyHint`.
- Sessions are reused per connector and closed on shutdown.

## Consequences

- Less provider-specific code where hosted MCP exists, without widening what the agent can do.
- Coverage gaps in a provider's MCP server (such as GitHub Deployments) still need direct API calls.
- Axiom's hosted MCP server is not used yet: its tools could not be checked without credentials.
