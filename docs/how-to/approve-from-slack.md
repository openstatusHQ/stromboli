# How to approve actions from Slack

With Slack buttons, a proposed machine action shows **Apply** and **Reject** in the investigation's message instead of a CLI command. The click goes through the same approval as the CLI, with the Slack user recorded as the approver (`@alice (Slack U0123)`).

## Prerequisites

- [Slack is set up](set-up-slack.md): `SLACK_BOT_TOKEN` and `SLACK_CHANNEL`.
- [Machine actions are enabled](enable-machine-actions.md): `STROMBOLI_ACTIONS_ENABLED=true`.
- stromboli's `/slack/interactions` is reachable from the internet.

## 1. Turn on interactivity

In your Slack app, **Interactivity & Shortcuts** → on, with Request URL:

```text
https://<stromboli>/slack/interactions
```

It is already on if you created the app from the manifest with your URL.

## 2. Set the signing secret

Copy **Basic Information** → **Signing Secret** into:

```sh
SLACK_SIGNING_SECRET=...
```

Requests without a valid Slack signature, or older than 5 minutes, are refused.

## 3. Restrict who can click

Strongly recommended. Without it, anyone who can see the channel can approve:

```sh
SLACK_APPROVERS=U0123,U0456   # Slack user IDs: profile → ⋮ → Copy member ID
```

Restart the server. It logs `Slack approval buttons are enabled` with the approvers, or a warning if anyone can click.

## What happens on a click

- **Apply** asks to confirm, showing the risk and the undo, then runs the action. The message shows it running, and the outcome is posted in the thread and replaces the buttons.
- **Reject** records the decision and posts it in the thread.
- Expired or superseded actions lose their buttons on the next redraw. A click on one is answered with why it cannot run.

Only machine actions have buttons. Text-only mitigations (rollback, failover…) are still applied by a human outside stromboli.

## See also

- [ADR 0014: approve actions from Slack](../adr/0014-slack-approval-buttons.md)
