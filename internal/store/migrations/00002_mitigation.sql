-- +goose Up
-- Mitigation proposed by the triage phase (opensre.v1.Mitigation as protojson),
-- published before the root-cause investigation finishes.
ALTER TABLE investigations ADD COLUMN mitigation jsonb;
ALTER TABLE investigations ADD COLUMN mitigated_at timestamptz;

-- +goose Down
ALTER TABLE investigations DROP COLUMN mitigated_at;
ALTER TABLE investigations DROP COLUMN mitigation;
