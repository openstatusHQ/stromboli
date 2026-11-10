# How to use stromboli from an MCP client

stromboli is also an MCP server. From Claude Code, Claude Desktop, Cursor or a coding agent, you can read what stromboli sees on a service, read an investigation's mitigation and report, or start one. MCP clients cannot approve machine actions or change the catalog or connectors.

There are two ways to connect: a local process over stdio, or HTTP on the server.

## Over stdio

The client launches `stromboli mcp`, which calls your server with the API token like any other CLI command. Nothing changes on the server.

Claude Code:

```sh
claude mcp add stromboli -e STROMBOLI_URL=https://stromboli.example.com -e STROMBOLI_API_TOKEN=... -- stromboli mcp
```

Any client configured with a command:

```json
{
  "mcpServers": {
    "stromboli": {
      "command": "stromboli",
      "args": ["mcp"],
      "env": { "STROMBOLI_URL": "https://stromboli.example.com", "STROMBOLI_API_TOKEN": "..." }
    }
  }
}
```

## Over HTTP

1. Enable the endpoint on the server (it requires `STROMBOLI_API_TOKEN`):

   ```sh
   STROMBOLI_MCP_ENABLED=true
   STROMBOLI_MCP_TOKEN=<openssl rand -hex 32>   # recommended
   ```

   With `STROMBOLI_MCP_TOKEN` set, `/mcp` accepts only that token, and that token reaches only the MCP tools, not the whole API. Without it, `/mcp` accepts the API token.

2. Restart the server. It logs `MCP server is enabled at /mcp`.

3. Add it to your client:

   ```sh
   claude mcp add --transport http stromboli https://stromboli.example.com/mcp \
     --header "Authorization: Bearer $STROMBOLI_MCP_TOKEN"
   ```

## Try it

Ask your client things like:

- "What does stromboli know about the `api` service?" (`get_service`, `get_brief`)
- "Show me the last investigations of `checker` and their root cause." (`list_investigations`, `get_investigation`)
- "Start a stromboli investigation of `api`: 5xx on /v1/monitors since 14:05, and tell me the mitigation." (`start_investigation`, `wait_investigation`)

`start_investigation` spends LLM tokens and posts to Slack, like a webhook would. Investigations started from MCP have source `mcp`, with the client's name in their alert.

## See also

- [MCP tools reference](../reference/mcp-tools.md)
- [ADR 0021: stromboli as an MCP server](../adr/0021-mcp-server.md)
