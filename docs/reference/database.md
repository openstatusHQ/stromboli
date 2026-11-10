# Database schema reference

All state lives in one Postgres database ([ADR 0004](../adr/0004-postgres-only-storage.md)). The CI and Compose files use Postgres 17. Migrations are in [`internal/store/migrations`](../../internal/store/migrations), applied with [goose](https://github.com/pressly/goose) on `stromboli serve` and by `stromboli migrate`. They only move forward. Queries are generated with sqlc from [`internal/store/queries`](../../internal/store/queries).

## Migrations

| Migration | |
|---|---|
| `00001_init.sql` | `services`, `runbooks`, `connectors`, `investigations`, `steps` |
| `00002_mitigation.sql` | `investigations.mitigation`, `investigations.mitigated_at` |
| `00003_actions.sql` | `actions` |
| `00004_resolved.sql` | `investigations.resolved_at` |

## Tables

### services

| Column | Type | |
|---|---|---|
| `name` | text, primary key | |
| `spec` | jsonb | the full catalog entry, `stromboli.v1.Service` as protojson |
| `created_at`, `updated_at` | timestamptz | |

### runbooks

| Column | Type | |
|---|---|---|
| `path` | text, primary key | e.g. `runbooks/api-5xx.md` |
| `content` | text | Markdown |
| `updated_at` | timestamptz | |

### connectors

| Column | Type | |
|---|---|---|
| `name` | text, primary key | |
| `kind` | text | `loki`, `axiom`, … |
| `config` | jsonb | non-secret settings |
| `secrets` | bytea | secret settings as a JSON object, encrypted with AES-256-GCM (nonce ‖ ciphertext) under the SHA-256 of `STROMBOLI_SECRET_KEY` |
| `created_at`, `updated_at` | timestamptz | |

### investigations

| Column | Type | |
|---|---|---|
| `id` | uuid, primary key | |
| `title`, `source` | text | |
| `fingerprint` | text, nullable | dedupe key, e.g. `openstatus:1234`, `alertmanager:<fp>`, `generic:<fp>` |
| `services` | text[] | |
| `status` | text | `running`, `completed`, `failed` |
| `alert` | jsonb | alert payload, `description`, `observed_at` |
| `mitigation` | jsonb, nullable | `stromboli.v1.Mitigation` as protojson |
| `mitigated_at` | timestamptz, nullable | first mitigation |
| `report` | jsonb, nullable | `stromboli.v1.Report` as protojson |
| `error` | text, nullable | |
| `slack_channel`, `slack_ts` | text, nullable | the Slack message, edited in place |
| `input_tokens`, `output_tokens` | bigint | |
| `created_at` | timestamptz | |
| `finished_at` | timestamptz, nullable | |
| `resolved_at` | timestamptz, nullable | set when the alert source reported the problem resolved; never reused for dedupe after that |

Indexes on `created_at DESC` and `(fingerprint, created_at DESC)`. Investigations still `running` at startup are marked `failed`.

### steps

The audit trail. Deleted with their investigation.

| Column | Type | |
|---|---|---|
| `investigation_id` | uuid | references `investigations` |
| `seq` | bigint | primary key with `investigation_id` |
| `kind` | text | `brief`, `tool_call`, `llm`, `mitigation`, `report`, `error` |
| `name` | text | |
| `input` | jsonb | |
| `output` | text | |
| `is_error` | boolean | |
| `duration_ms` | bigint | |
| `created_at` | timestamptz | |

### actions

Deleted with their investigation.

| Column | Type | |
|---|---|---|
| `id` | uuid, primary key | |
| `investigation_id` | uuid | references `investigations` |
| `service` | text | |
| `kind` | text | `fly_machine` |
| `spec` | jsonb | `stromboli.v1.MachineAction` as protojson |
| `connector` | text | the `fly_machines` connector |
| `status` | text | `proposed`, `running`, `succeeded`, `failed`, `rejected`, `expired`, `superseded` |
| `decided_by`, `note`, `result`, `error` | text, nullable | |
| `created_at` | timestamptz | |
| `expires_at` | timestamptz | |
| `decided_at`, `finished_at` | timestamptz, nullable | |

Indexes on `(investigation_id, created_at)` and `created_at DESC`. Actions still `running` at startup are marked `failed`.
