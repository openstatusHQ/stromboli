-- +goose Up
-- Set when the alert source reports the problem resolved (e.g. openstatus
-- "recovered"). A resolved investigation is never reused for a new alert.
ALTER TABLE investigations ADD COLUMN resolved_at timestamptz;

-- +goose Down
ALTER TABLE investigations DROP COLUMN resolved_at;
