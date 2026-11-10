# How to enable machine actions on Fly.io

By default every mitigation is text for a human to apply. For services on Fly.io, you can let stromboli run some machine actions itself (`restart`, `clone`, `cordon`, `uncordon`, `stop`, `start`), always after a named human approves each one.

Read the [safety model](../explanation/safety-model.md) first: this is the one place where stromboli changes production.

## 1. Turn actions on for the instance

```sh
STROMBOLI_ACTIONS_ENABLED=true   # requires STROMBOLI_API_TOKEN
STROMBOLI_ACTION_TTL=30m         # how long a proposed action can be approved (default 30m)
```

The server refuses to start with actions enabled and no API token.

## 2. Add a connector that can act on machines

Keep it separate from the read-only `fly` metrics connector. Scope the token to the app:

```sh
stromboli connector add fly-deploy fly_machines org=<org-slug> token="$(fly tokens create deploy -a <app>)"
```

## 3. Allow actions per service

```yaml
- name: checker
  fly:
    app: openstatus-checker              # default: the app="..." metrics selector, then the service name
    actions: fly-deploy                  # the fly_machines connector; without it, mitigations stay text
    allow: [restart, cordon, uncordon]   # default: restart, clone, cordon, uncordon
```

`stop` and `start` are never allowed unless listed. Import the file with `stromboli catalog import`.

## 4. Approve or reject proposed actions

When the agent's mitigation fits an allowed action, it attaches one, and stromboli stores it as **proposed**. Find them:

```sh
stromboli actions --status proposed
```

Look at one, then decide:

```sh
stromboli actions show <action-id>
stromboli actions approve <action-id> --as alice --note "ok from the incident call"   # asks to confirm; --yes skips
stromboli actions reject <action-id> --as alice --note "the db is the cause"
```

`--as` (or `STROMBOLI_USER`) is your name, recorded with the decision. Approving runs the action right away and prints what it did, machine by machine, with how to undo it. The outcome is also posted in the investigation's Slack thread.

> [!IMPORTANT]
> Anyone with the API token can approve, under any name. To restrict who can approve, use [Slack buttons](approve-from-slack.md) with `SLACK_APPROVERS`.

## Check it

On startup, the server logs `machine actions are enabled`. In a brief of the service (`stromboli brief checker`), the **Fly.io** section says which actions stromboli can run.

If a mitigation that names a machine command comes without an action, stromboli refused it and published the mitigation as a suggestion. The usual reasons: the action is not in `fly.allow`, `fly.actions` is missing or not a `fly_machines` connector, a machine ID is not one of the app's machines, more than 10 machines, or a `clone` of a machine with a volume. The [guardrails](../reference/machine-actions.md#guardrails) list them all.

## See also

- [Machine actions reference](../reference/machine-actions.md): statuses, guardrails, what each action does
- [ADR 0013: approved machine actions, Fly.io only](../adr/0013-approved-fly-machine-actions.md)
