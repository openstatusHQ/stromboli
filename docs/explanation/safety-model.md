# Safety model

stromboli points a language model at your production data during an incident. That only works if you can trust what it is able to do. The rule is simple: **stromboli suggests, humans act** ([ADR 0006](../adr/0006-suggest-dont-execute.md)). This page explains how that rule is enforced, the one opt-in exception, and what protects your data and credentials.

## Read-only by construction

stromboli does not rely on the model behaving well. It removes the means to misbehave:

- **Every tool the model can call is read-only.** There is no tool that writes, deploys, restarts or executes anything. The model's only outputs are a mitigation and a report.
- **Connectors take read-only credentials.** A read-only Fly org token, a viewer role on Google Cloud, a read-only GitHub token. Even a bug in stromboli could not use them to change anything.
- **The model never talks to your systems.** stromboli calls them from code and hands the model compact, aggregated evidence. When a provider is reached through its MCP server, each connector may call only an allow-listed set of tools, and for servers that annotate their tools, connecting fails unless every allowed tool is marked read-only ([Data access](data-access.md)).
- **Data is treated as data.** The system prompt tells the model that log lines and commit messages are data from the systems it investigates, not instructions. Since it has no write tools, a prompt injection in a log line can at worst mislead a report that a human reads.

## The exception: approved machine actions

Some mitigations are both common and safe to automate: restarting a stuck machine, cordoning a bad one, cloning one to add capacity. For Fly.io services, stromboli can run those, under layered guardrails ([ADR 0013](../adr/0013-approved-fly-machine-actions.md)):

1. **Off by default.** `STROMBOLI_ACTIONS_ENABLED=true` turns it on, and the server refuses to start that way without an API token.
2. **A separate credential.** Actions use a `fly_machines` connector, distinct from the read-only metrics token, and named per service in the catalog.
3. **Allow-listed per service.** Only listed actions can run; `stop` and `start` are never allowed by default because they remove or add capacity.
4. **Checked against reality.** Every machine must belong to the service's app, at most 10 per action, and a machine with a volume is never cloned. These checks run when the action is proposed *and* again when it is approved.
5. **A named human approves, in time.** Nothing runs on its own. The approver's name and note are recorded, and an action nobody approves expires (`STROMBOLI_ACTION_TTL`). A revised mitigation supersedes a pending action.
6. **Runs once, carefully.** Approval claims the action atomically, so it cannot run twice. Machines are handled one at a time; a restart waits for the machine to come back and stops at the first failure, so a rolling restart never takes down more than one machine that did not return.
7. **Undo is part of the result.** Each machine's line says how to revert it.

The model's role stays the same: it proposes. Even with actions enabled it has no tool that runs anything, and it is told so.

**Who can approve** is the weak point, and the docs say so plainly. Anyone with the API token can approve under any name: the name is recorded, not verified. Slack buttons ([ADR 0014](../adr/0014-slack-approval-buttons.md)) verify identity through Slack, check Slack's request signature, and can be limited to specific users with `SLACK_APPROVERS`. MCP clients, which are driven by models, cannot approve at all.

## Protecting data

- **Nothing is ingested.** Logs and metrics stay in your systems; stromboli queries them when an investigation runs.
- **Redaction before the model.** Log lines, patterns, deploy titles, commit messages and diffs pass through a redaction filter that removes bearer tokens, JWTs, cloud and API keys, Slack tokens, values of keys named like secrets, credentials in URLs, and email addresses.
- **Size caps.** Tool output is aggregated and capped (24 KiB per tool call), which limits both cost and how much raw data reaches the model provider.
- **Your model provider.** Prompts go to the provider you configure. To keep them in your network, use Ollama or any OpenAI-compatible endpoint you run.

## Protecting credentials

- **Encrypted at rest.** Connector secrets are encrypted with AES-256-GCM under a key derived from `STROMBOLI_SECRET_KEY`, and never returned by the API.
- **Off the command line.** The CLI prompts for secrets, or reads them from a file or stdin, and warns when one is typed inline.
- **Separate tokens.** The API token, an optional webhook token (so the token pasted into alerting tools cannot call the API), and an optional MCP token (which reaches only the MCP tools).

## What stromboli does not do

It does not write code, open pull requests, run arbitrary commands, or apply text mitigations like rollbacks or failovers. It hands context to an engineer, or to a coding agent through its MCP server, and lets them act.
