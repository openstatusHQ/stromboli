# Reference

Reference pages describe stromboli as it is: every command, setting, field and message, without telling you what to do with them. For tasks, see the [how-to guides](../how-to/README.md); for the reasoning behind the design, the [explanation](../explanation/README.md).

| Page | Describes |
|---|---|
| [CLI](cli.md) | every `stromboli` command, flag, environment variable and exit code |
| [Configuration](configuration.md) | every environment variable the server reads |
| [Service catalog](catalog.md) | the catalog YAML schema and its validation rules |
| [Connectors](connectors.md) | connector kinds, config keys, selectors and discovery |
| [Metric presets](metric-presets.md) | named metrics of the `fly` and `gcp_monitoring` connectors |
| [Alert webhooks](webhooks.md) | `/webhooks/*`: authentication, payloads, service matching, fingerprints, responses |
| [HTTP and ConnectRPC API](api.md) | HTTP routes, the four ConnectRPC services and their messages |
| [Agent](agent.md) | the brief, the agent's tools, mitigation kinds, report fields and limits |
| [Machine actions](machine-actions.md) | actions, statuses, guardrails and execution |
| [MCP tools](mcp-tools.md) | the tools stromboli serves to MCP clients |
| [Database schema](database.md) | the Postgres tables and migrations |
