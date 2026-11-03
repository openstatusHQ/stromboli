-- +goose Up
-- Executable mitigations and their audit trail: proposed by an investigation,
-- run only after a human approves them.
CREATE TABLE actions (
    id               uuid PRIMARY KEY,
    investigation_id uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
    service          text NOT NULL,
    -- "fly_machine" for now.
    kind             text NOT NULL,
    -- opensre.v1.MachineAction as protojson.
    spec             jsonb NOT NULL,
    connector        text NOT NULL,
    -- proposed, running, succeeded, failed, rejected, expired, superseded.
    status           text NOT NULL,
    decided_by       text,
    note             text,
    result           text,
    error            text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    decided_at       timestamptz,
    finished_at      timestamptz
);

CREATE INDEX actions_investigation_idx ON actions (investigation_id, created_at);
CREATE INDEX actions_created_at_idx ON actions (created_at DESC);

-- +goose Down
DROP TABLE actions;
