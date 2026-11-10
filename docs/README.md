# stromboli documentation

stromboli is an open-source AI SRE agent you host yourself. When an alert fires, it gathers context from your logs, metrics and deploys, proposes a mitigation within a few tool calls, then looks for the root cause and posts both to Slack.

These docs follow the [Diátaxis](https://diataxis.fr) framework. Each section answers a different need, so pick the one that matches what you are trying to do:

| | Section | Read it when you want to… |
|---|---|---|
| 🎓 | [**Tutorials**](tutorials/README.md) | **learn** stromboli by doing, step by step, with a result at the end |
| 🛠️ | [**How-to guides**](how-to/README.md) | **get a specific job done**: connect Axiom, set up Slack, turn on machine actions… |
| 📖 | [**Reference**](reference/README.md) | **look something up**: a CLI flag, an environment variable, a catalog field, an API message |
| 💡 | [**Explanation**](explanation/README.md) | **understand** how stromboli works and why it is built this way |

## Start here

New to stromboli? Do the tutorials in order:

1. [Your first investigation, locally](tutorials/01-first-investigation.md): run stromboli with a demo app that breaks after three minutes, and watch it investigate.
2. [Connect a real service](tutorials/02-connect-a-real-service.md): deploy stromboli, connect your own logs, describe a service and send it an alert.

## Map

### Tutorials

- [Your first investigation, locally](tutorials/01-first-investigation.md)
- [Connect a real service](tutorials/02-connect-a-real-service.md)

### How-to guides

- Install and run: [self-host in production](how-to/self-host.md), [deploy on Railway](how-to/deploy-on-railway.md), [deploy on Fly.io](how-to/deploy-on-fly.md)
- Connect data: [logs](how-to/connect-logs.md), [metrics](how-to/connect-metrics.md), [deploys from GitHub](how-to/connect-github.md), [ship Fly.io logs](how-to/ship-fly-logs.md)
- Describe your system: [manage the catalog in git](how-to/manage-the-catalog.md)
- Receive alerts and report: [send alerts to stromboli](how-to/send-alerts.md), [set up Slack](how-to/set-up-slack.md)
- Tune: [choose models](how-to/choose-models.md), [enable health checks](how-to/enable-health-checks.md)
- Act: [enable machine actions](how-to/enable-machine-actions.md), [approve actions from Slack](how-to/approve-from-slack.md)
- Integrate: [use stromboli from an MCP client](how-to/use-the-mcp-server.md), [script the CLI](how-to/script-the-cli.md)
- [Troubleshoot](how-to/troubleshoot.md)
- [Develop stromboli](how-to/develop.md)

### Reference

- [CLI](reference/cli.md)
- [Configuration (environment variables)](reference/configuration.md)
- [Service catalog](reference/catalog.md)
- [Connectors](reference/connectors.md)
- [Metric presets](reference/metric-presets.md)
- [Alert webhooks](reference/webhooks.md)
- [HTTP and ConnectRPC API](reference/api.md)
- [Agent tools, mitigations and reports](reference/agent.md)
- [Machine actions](reference/machine-actions.md)
- [MCP server tools](reference/mcp-tools.md)
- [Database schema](reference/database.md)

### Explanation

- [Architecture](explanation/architecture.md)
- [The life of an investigation](explanation/investigation-lifecycle.md)
- [Mitigate first, investigate second](explanation/mitigate-first.md)
- [Safety model](explanation/safety-model.md)
- [Data access: on demand, MCP first](explanation/data-access.md)
- [The service catalog](explanation/service-catalog.md)
- [Architecture decision records](adr/README.md)
