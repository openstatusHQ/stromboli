# How to connect Vercel

stromboli reads a Vercel project's **runtime logs** and **deployments** through Vercel's MCP server.

## 1. Log in

Vercel's MCP server takes no access token: stromboli logs in with OAuth, once, in your browser. Run this in a terminal, with your team's slug (as in `vercel.com/<slug>`) or ID:

```sh
stromboli connector add vercel vercel team_id=<team-slug>
```

It opens Vercel's consent page; approve it, and the connector is saved and tested. The test checks that the login can read the team's projects, so a wrong `team_id` fails here. If the browser runs on another machine (over SSH, for example), open the printed URL there and paste back the address it redirects to.

The login has the access of your Vercel user; stromboli may only call `list_projects`, `list_deployments` and `get_runtime_logs`. The server keeps the connector working on its own. If the login expires or you revoke it in Vercel, calls fail with `run stromboli connector login vercel`: run it to log in again.

## 2. Add the services

Let the connector propose one service per project:

```sh
stromboli catalog discover vercel > vercel.yaml
```

Then add owners, and the deploys of each project:

```yaml
- name: web
  owners: ["@you"]
  logs:
    source: vercel
    selector: web environment=production   # project name or ID; environment is optional
  deploys:
    source: vercel
    project: web              # project name or ID
    environment: production   # optional: production (default) or preview
    branch: main              # optional: only deploys of this branch
```

```sh
stromboli catalog import vercel.yaml
```

## 3. Check it

```sh
stromboli catalog validate --check-connectors
stromboli brief web --since 1h
```

The **Logs** section shows the error patterns of the project's runtime logs, and **Recent changes** its deployments with their commit, author and state (`ready`, `error`…).

## Limits

- Vercel's MCP server returns log entries that carry a message: what your functions print, and errors. Plain request records (the `GET 200 /` rows without a message in the dashboard) are not included, so a project that serves only static or cached pages shows 0 lines, and 5xx responses without a logged error are not seen. To get request records too, send the logs to Axiom (below).
- Vercel returns at most 100 log lines per call. Counts without a filter are exact; counts of error-like lines stop at 500.
- Logs go back as far as your Vercel plan keeps them. For longer history, also send them to Axiom with the [Axiom integration](https://vercel.com/integrations/axiom) and [connect Axiom](connect-logs.md#axiom) instead.

## See also

- [Connectors reference](../reference/connectors.md#vercel)
- [Catalog reference: `deploys`](../reference/catalog.md#deploys)
- [ADR 0022: OAuth for remote MCP servers](../adr/0022-oauth-for-remote-mcp-servers.md)
