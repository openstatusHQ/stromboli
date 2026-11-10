# How to set up Slack

stromboli posts one message per investigation: the mitigation as soon as it is proposed, then the report, updated in place. It also replies in the message's thread so on-call is notified, and posts machine action outcomes there. The bot only needs the `chat:write` scope.

## 1. Create the app from the manifest

The app is described in [`deploy/slack/manifest.json`](../../deploy/slack/manifest.json).

1. Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From a manifest**, and pick your workspace.
2. Paste the manifest. Before creating the app, replace `stromboli.example.com` in `request_url` with stromboli's public URL, or delete the `interactivity` block if you won't use [Apply/Reject buttons](approve-from-slack.md).

## 2. Install it

**Install App** → **Install to Workspace**, then copy the **Bot User OAuth Token** (`xoxb-...`).

## 3. Invite the bot

In the channel where reports should go:

```text
/invite @stromboli
```

## 4. Configure stromboli

Set these variables and restart the server:

```sh
SLACK_BOT_TOKEN=xoxb-...
SLACK_CHANNEL=#incidents     # a name or a channel ID (C0123...)
```

Both are required: with only one, Slack stays off and the server logs `Slack is not configured`.

## 5. Check it

```sh
stromboli investigate <service> --description "testing Slack"
```

The message should appear in the channel. If it doesn't, look at the server logs:

| Log | Fix |
|---|---|
| `not_in_channel` | the bot is not in the channel: step 3 |
| `invalid_auth` | wrong token: step 2 |
| `channel_not_found` | wrong `SLACK_CHANNEL`, or a private channel the bot was not invited to |

## Optional

- [Approve machine actions from Slack](approve-from-slack.md) with `SLACK_SIGNING_SECRET` and `SLACK_APPROVERS`.
- Post an all-clear after healthy [health check](enable-health-checks.md) runs with `STROMBOLI_HEALTHCHECK_SUMMARY=true`.
