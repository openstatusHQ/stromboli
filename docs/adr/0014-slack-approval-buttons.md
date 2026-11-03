# 0014. Approve actions from Slack

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

On-call reads the investigation in Slack. Switching to a terminal to approve a proposed action ([0013](0013-approved-fly-machine-actions.md)) costs time, and an API-token approval records a name that nobody verified.

## Decision

- With `SLACK_SIGNING_SECRET` set, a proposed machine action shows **Apply** and **Reject** buttons in the investigation's Slack message. Apply asks to confirm, showing the risk and the undo.
- Clicks go through the same approve and reject path as the CLI, with the Slack user recorded as the approver.
- Requests without a valid Slack signature, or older than 5 minutes, are refused. `SLACK_APPROVERS` limits the buttons to listed Slack user IDs.
- Expired or superseded actions lose their buttons; a click on one is answered with why it cannot run.
- Only machine actions have buttons. Text mitigations are still applied by a human outside opensre.

## Consequences

- Approval happens where the incident is discussed, with an identity Slack vouches for.
- Without `SLACK_APPROVERS`, anyone who can see the channel can approve.
- Next: limit approvers to the service's owners from the catalog.
