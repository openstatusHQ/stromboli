-- name: ListServices :many
SELECT * FROM services ORDER BY name;

-- name: GetService :one
SELECT * FROM services WHERE name = $1;

-- name: UpsertService :exec
INSERT INTO services (name, spec) VALUES ($1, $2)
ON CONFLICT (name) DO UPDATE SET spec = EXCLUDED.spec, updated_at = now();

-- name: DeleteService :execrows
DELETE FROM services WHERE name = $1;

-- name: DeleteServicesNotIn :execrows
DELETE FROM services WHERE NOT (name = ANY(@names::text[]));

-- name: ListRunbooks :many
SELECT * FROM runbooks ORDER BY path;

-- name: GetRunbook :one
SELECT * FROM runbooks WHERE path = $1;

-- name: UpsertRunbook :exec
INSERT INTO runbooks (path, content) VALUES ($1, $2)
ON CONFLICT (path) DO UPDATE SET content = EXCLUDED.content, updated_at = now();

-- name: DeleteRunbooksNotIn :execrows
DELETE FROM runbooks WHERE NOT (path = ANY(@paths::text[]));

-- name: ListConnectors :many
SELECT * FROM connectors ORDER BY name;

-- name: GetConnector :one
SELECT * FROM connectors WHERE name = $1;

-- name: CreateConnector :exec
INSERT INTO connectors (name, kind, config, secrets) VALUES ($1, $2, $3, $4);

-- name: ReplaceConnector :exec
INSERT INTO connectors (name, kind, config, secrets) VALUES ($1, $2, $3, $4)
ON CONFLICT (name) DO UPDATE SET kind = EXCLUDED.kind, config = EXCLUDED.config,
    secrets = EXCLUDED.secrets, updated_at = now();

-- name: UpdateConnectorSecrets :execrows
-- Saves a rotated OAuth refresh token, unless the connector was logged in
-- again (authorized_at changed) since it was read.
UPDATE connectors SET secrets = $2, updated_at = now()
WHERE name = $1 AND config->>'authorized_at' = @authorized_at::text;

-- name: DeleteConnector :execrows
DELETE FROM connectors WHERE name = $1;

-- name: CreateInvestigation :one
INSERT INTO investigations (id, title, source, fingerprint, services, status, alert)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetInvestigation :one
SELECT * FROM investigations WHERE id = $1;

-- name: ListInvestigations :many
SELECT * FROM investigations ORDER BY created_at DESC LIMIT $1;

-- name: FindOpenInvestigation :one
-- A running investigation always absorbs a repeat of its alert. A finished one
-- does only within the window, and never once the alert resolved or one of its
-- actions succeeded: the alert firing again is then a new incident.
SELECT * FROM investigations i
WHERE i.fingerprint = $1 AND i.resolved_at IS NULL AND (
    i.status = 'running'
    OR (i.finished_at > now() - make_interval(mins => @window_minutes::int)
        AND NOT EXISTS (SELECT 1 FROM actions a WHERE a.investigation_id = i.id AND a.status = 'succeeded'))
)
ORDER BY i.created_at DESC
LIMIT 1;

-- name: ResolveInvestigations :execrows
UPDATE investigations SET resolved_at = now()
WHERE fingerprint = $1 AND resolved_at IS NULL;

-- name: SetInvestigationSlack :exec
UPDATE investigations SET slack_channel = $2, slack_ts = $3 WHERE id = $1;

-- name: AddInvestigationTokens :exec
UPDATE investigations SET input_tokens = input_tokens + $2, output_tokens = output_tokens + $3 WHERE id = $1;

-- name: SetInvestigationMitigation :exec
UPDATE investigations SET mitigation = $2, mitigated_at = COALESCE(mitigated_at, now()) WHERE id = $1;

-- name: FinishInvestigation :exec
UPDATE investigations SET status = $2, report = $3, error = $4, finished_at = now() WHERE id = $1;

-- name: FailRunningInvestigations :execrows
UPDATE investigations SET status = 'failed', error = 'server restarted during investigation', finished_at = now()
WHERE status = 'running';

-- name: InsertStep :exec
INSERT INTO steps (investigation_id, seq, kind, name, input, output, is_error, duration_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListSteps :many
SELECT * FROM steps WHERE investigation_id = $1 AND seq > $2 ORDER BY seq;

-- name: SearchInvestigations :many
SELECT * FROM investigations
WHERE report IS NOT NULL
  AND (@service::text = '' OR @service::text = ANY(services))
  AND (@query::text = '' OR title ILIKE '%' || @query::text || '%' OR report::text ILIKE '%' || @query::text || '%')
ORDER BY created_at DESC
LIMIT $1;

-- name: CreateAction :one
INSERT INTO actions (id, investigation_id, service, kind, spec, connector, status, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, 'proposed', $7)
RETURNING *;

-- name: GetAction :one
SELECT * FROM actions WHERE id = $1;

-- name: ListActions :many
SELECT * FROM actions
WHERE (sqlc.narg('investigation_id')::uuid IS NULL OR investigation_id = sqlc.narg('investigation_id')::uuid)
  AND (@status::text = '' OR status = @status::text)
ORDER BY created_at DESC
LIMIT $1;

-- name: ExpireActions :execrows
UPDATE actions SET status = 'expired' WHERE status = 'proposed' AND expires_at <= now();

-- name: SupersedeActions :execrows
UPDATE actions SET status = 'superseded' WHERE investigation_id = $1 AND status = 'proposed';

-- Claiming is atomic: only one approval of a proposed, unexpired action wins.
-- name: ClaimAction :one
UPDATE actions SET status = 'running', decided_by = $2, note = $3, decided_at = now()
WHERE id = $1 AND status = 'proposed' AND expires_at > now()
RETURNING *;

-- name: RejectAction :one
UPDATE actions SET status = 'rejected', decided_by = $2, note = $3, decided_at = now()
WHERE id = $1 AND status = 'proposed'
RETURNING *;

-- name: FinishAction :one
UPDATE actions SET status = $2, result = $3, error = $4, finished_at = now()
WHERE id = $1
RETURNING *;

-- name: FailRunningActions :execrows
UPDATE actions SET status = 'failed', finished_at = now(),
    error = 'server restarted while the action was running: check the machines'
WHERE status = 'running';
