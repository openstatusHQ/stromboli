# Changelog

All notable changes to stromboli are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and stromboli uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Until 1.0.0, a minor version (0.x.0) may include breaking changes to the configuration, catalog format or API; they are called out under **Changed** or **Removed**. Patch versions (0.x.y) only fix bugs.

## [Unreleased]

## [0.1.0] - 2026-11-13

The first release.

### Added

- **Investigations.** When an alert fires, stromboli gathers context, proposes a mitigation (rollback, restart, scale up…) within a few tool calls, then tests hypotheses against logs, metrics and deploys and writes a structured root-cause report.
- **Alert webhooks** with deduplication: a re-fired alert after a fix or recovery opens a new incident.
- **Slack** integration: scannable investigation messages and threads, with Apply/Reject approval buttons for proposed mitigations.
- **Service catalog** owned by stromboli, with tiers, owners, dependencies and sources, plus `catalog discover` from a log backend or from Fly.io.
- **Connectors**, MCP-first, queried on demand (no data ingestion):
  - logs: Loki, Axiom (MCP, with OAuth), CloudWatch Logs, Google Cloud Logging, Vercel, and Fly.io logs shipped by `flyio/log-shipper`;
  - metrics: Fly.io machine metrics, Prometheus, Google Cloud Monitoring;
  - deploys and commits: GitHub, Vercel;
  - OAuth for remote MCP servers.
- **Fly.io machine actions** (restart, clone, cordon…), opt-in and only after a named human approves them.
- **Health checks** on a schedule, including services with no metrics, with Slack warnings and an optional all-clear.
- **Any LLM**: Claude by default; OpenAI, Gemini, DeepSeek, Ollama, OpenRouter and Vercel AI Gateway, with per-task models.
- **API** over ConnectRPC, an **MCP server**, and a `stromboli` CLI that follows clig.dev.
- Postgres-only storage, with migrations on startup.
- Deployment with Docker Compose, Railway and Fly.io; multi-arch images on `ghcr.io/openstatushq/stromboli`, binaries for Linux, macOS and Windows, and a Homebrew cask.

[Unreleased]: https://github.com/openstatushq/stromboli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/openstatushq/stromboli/releases/tag/v0.1.0
