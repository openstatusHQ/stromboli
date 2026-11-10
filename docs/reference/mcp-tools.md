# MCP tools reference

stromboli serves these tools to MCP clients, over stdio with `stromboli mcp` or over streamable HTTP at `/mcp` with `STROMBOLI_MCP_ENABLED=true`. They call the same services as the [API](api.md). The definitions are pinned in [`internal/mcpserver/testdata/tools.golden.json`](../../internal/mcpserver/testdata/tools.golden.json).

| Tool | Arguments (**required**) | Read-only |
|---|---|---|
| `list_services` | | yes |
| `get_service` | **`name`** | yes |
| `get_brief` | **`service`**, `since` (duration, default `1h`, max `168h`) | yes |
| `list_investigations` | `service`, `status` (`running`, `completed`, `failed`), `limit` (default 10, max 50) | yes |
| `get_investigation` | **`id`**, `include_steps` (bool), `after_seq` (with `include_steps`) | yes |
| `wait_investigation` | **`id`**, `until` (`mitigation` default, or `finished`), `timeout_s` (default 60, max 300) | yes |
| `start_investigation` | **`service`**, `title` (default `Investigation of <service>`), `description` | no |
| `list_actions` | `investigation_id`, `status`, `limit` (default 20, max 50) | yes |
| `get_action` | **`id`** | yes |

## Tools

**`list_services`**: every catalog service with its tier, owners and direct dependencies.

**`get_service`**: a service's catalog entry (owners, runtime, where its logs, metrics and deploys live, runbooks) and the services upstream and downstream of it.

**`get_brief`**: the [brief](agent.md#the-brief), built without the LLM. Free.

**`list_investigations`**: recent investigations, newest first.

**`get_investigation`**: status, the mitigation proposed first, and the report once finished. With `include_steps`, the audit trail, paged with `after_seq`.

**`wait_investigation`**: waits until the mitigation is published (`until=mitigation`) or the investigation finished (`until=finished`), for at most `timeout_s`, sending progress notifications as steps happen. Returns what `get_investigation` returns; call it again if still running.

**`start_investigation`**: starts an investigation and returns its ID right away. It spends LLM tokens and posts to Slack, but only reads from production. The investigation's source is `mcp` and its alert holds the client's name (`mcp_client`).

**`list_actions`**, **`get_action`**: proposed machine actions, their status, who decided them and their result. Approving needs a named human, in the CLI or Slack.

## Authentication

| Transport | Credential |
|---|---|
| stdio (`stromboli mcp`) | the CLI's `STROMBOLI_API_TOKEN` (or `--token-file`), sent to `--url` |
| HTTP (`/mcp`) | `Authorization: Bearer <token>`: `STROMBOLI_MCP_TOKEN` when set (it reaches only these tools), else `STROMBOLI_API_TOKEN` |

`STROMBOLI_MCP_ENABLED=true` requires `STROMBOLI_API_TOKEN`: the endpoint is never served unauthenticated.
