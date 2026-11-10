# How-to guides

How-to guides are recipes for a specific job. They assume you know the basics (if not, start with the [tutorials](../tutorials/README.md)) and they skip explanations: follow the links to the [explanation](../explanation/README.md) pages for the why, and to the [reference](../reference/README.md) for every option.

## Install and run

- [Self-host in production](self-host.md): requirements, install options, the production checklist, upgrades and rollbacks
- [Deploy on Railway](deploy-on-railway.md)
- [Deploy on Fly.io](deploy-on-fly.md)

## Connect your data

- [Connect a log store](connect-logs.md): Loki, Axiom, CloudWatch, Google Cloud Logging
- [Connect metrics](connect-metrics.md): Prometheus-compatible, Fly.io, Google Cloud Monitoring
- [Connect deploys and commits from GitHub](connect-github.md)
- [Ship Fly.io logs to a log store](ship-fly-logs.md)

## Describe your system

- [Manage the service catalog in git](manage-the-catalog.md)

## Alerts and reports

- [Send alerts to stromboli](send-alerts.md): openstatus, Alertmanager, Grafana, anything else
- [Set up Slack](set-up-slack.md)

## Tune investigations

- [Choose models](choose-models.md): providers, per-phase models, effort
- [Enable health checks](enable-health-checks.md) for problems that never fire an alert

## Let stromboli act

- [Enable machine actions on Fly.io](enable-machine-actions.md)
- [Approve actions from Slack](approve-from-slack.md)

## Integrate

- [Use stromboli from an MCP client](use-the-mcp-server.md): Claude Code, Claude Desktop, Cursor
- [Script the CLI](script-the-cli.md): CI, JSON output, exit codes

## When something is wrong

- [Troubleshoot](troubleshoot.md)

## Work on stromboli

- [Develop stromboli](develop.md): run from source, test, regenerate code
