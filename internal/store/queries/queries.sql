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
SELECT * FROM investigations
WHERE fingerprint = $1 AND (status = 'running' OR finished_at > now() - make_interval(mins => @window_minutes::int))
ORDER BY created_at DESC
LIMIT 1;

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
