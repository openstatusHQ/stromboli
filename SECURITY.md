# Security policy

stromboli holds credentials to your logs, metrics and deploys and, when machine actions are enabled, can act on Fly.io machines. We take vulnerabilities seriously.

## Reporting a vulnerability

**Please don't open a public issue.** Report it privately through [GitHub security advisories](https://github.com/openstatushq/stromboli/security/advisories/new).

Include what you found, how to reproduce it and the version affected (`stromboli --version` or the image tag). We aim to acknowledge reports within 3 business days and to keep you updated until a fix is released. We are happy to credit you in the advisory.

## Supported versions

Security fixes go into the latest release. Self-hosters should keep up with releases; see [upgrading](docs/how-to/self-host.md#upgrading).

## Hardening your instance

See the [production checklist](docs/how-to/self-host.md#production-checklist). In short:

- set `STROMBOLI_API_TOKEN` (without it the API is unauthenticated) and a separate `STROMBOLI_WEBHOOK_TOKEN`
- serve stromboli behind TLS
- give connectors read-only credentials
- keep `STROMBOLI_ACTIONS_ENABLED=false` unless you need approved machine actions, and limit `SLACK_APPROVERS`
