# Machine actions reference

A machine action is a Fly.io Machines API call that an investigation proposes as part of its mitigation and that stromboli runs only after a named human approves it. The code is in [`internal/action`](../../internal/action).

## Actions

| Action | Fly Machines API | Waits for `started` | Undo, as reported in the result |
|---|---|---|---|
| `restart` | restart the machine | yes | none needed |
| `start` | start a stopped machine | yes | `fly machine stop <id> -a <app>` |
| `stop` | stop the machine | no | `fly machine start <id> -a <app>` |
| `cordon` | stop routing traffic to it; it keeps running | no | `fly machine uncordon <id> -a <app>` |
| `uncordon` | route traffic to it again | no | `fly machine cordon <id> -a <app>` |
| `clone` | create a copy, in `region` or the source's region | yes (the new machine) | `fly machine destroy <new-id> -a <app> --force` |

Allowed by default (empty `fly.allow`): `restart`, `clone`, `cordon`, `uncordon`. `stop` and `start` must be listed in `fly.allow`.

## Statuses

```text
proposed ──approve──▶ running ──▶ succeeded
    │                        └──▶ failed
    ├──reject──▶ rejected
    ├──TTL──▶ expired
    └──revised mitigation──▶ superseded
```

| Status | |
|---|---|
| `proposed` | waiting for a human, until `expires_at` (`STROMBOLI_ACTION_TTL` after creation) |
| `running` | approved and executing |
| `succeeded` | every machine was handled |
| `failed` | a call failed or a machine did not come up; `result` lists what was done before |
| `rejected` | a human rejected it |
| `expired` | nobody decided before `expires_at`. Applied when actions are read |
| `superseded` | the investigation revised its mitigation before anyone decided |

Actions that are `running` when the server restarts are marked `failed`.

## Guardrails

Checked when the action is **proposed** and again when it is **approved**:

1. `STROMBOLI_ACTIONS_ENABLED=true` (which requires `STROMBOLI_API_TOKEN`).
2. The service runs on Fly.io (`runtime: fly.io` or a `fly` block).
3. The service's `fly.actions` names a connector, and it is a `fly_machines` connector.
4. The action is one of the six above and is in the service's allow-list.
5. 1 to 10 distinct machine IDs (`service` is required when the investigation covers several services).
6. Every machine belongs to the service's Fly app, as listed by the Machines API.
7. `clone` never targets a machine with a volume: the clone would start with an empty volume.
8. The app has not changed in the catalog since the proposal, and the service still uses the same connector.

Then, at approval:

- an approver name is required;
- the action must still be `proposed` and not expired;
- it is claimed atomically, so it runs at most once even if two people approve at the same time.

## Execution

Machines are handled one at a time, in order. `restart`, `start` and `clone` wait up to 60 seconds for the machine to reach `started`. Execution stops at the first failure and reports how many machines were left untouched, so a rolling restart never takes down more than one machine that did not come back. A whole action is bounded to 10 minutes.

The result is one line per machine, for example:

```text
restarted 148e123b (started)
cloned 148e123b → 3d8d9a1c in ams (started) · undo: fly machine destroy 3d8d9a1c -a checker --force
```

## Records and notifications

Each action stores who decided (`decided_by`), when (`decided_at`), their `note` (or rejection reason), the `result`, the `error` and `finished_at`. With Slack, the outcome (or rejection) is posted in the investigation's thread; with buttons, the message shows the action running and then replaces the buttons with the outcome.

Ways to decide:

| Where | Approver recorded |
|---|---|
| `stromboli actions approve` / `reject` | `--as` or `STROMBOLI_USER`, as given |
| `ActionService/ApproveAction` / `RejectAction` | `approver`, as given |
| Slack Apply / Reject | `@<name> (Slack <user id>)`; limited to `SLACK_APPROVERS` when set |

MCP clients can list and read actions but cannot decide them.
