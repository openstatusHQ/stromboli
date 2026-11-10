-- +goose Up
CREATE TABLE services (
    name       text PRIMARY KEY,
    -- Full catalog entry as protojson (stromboli.v1.Service).
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE runbooks (
    path       text PRIMARY KEY,
    content    text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE connectors (
    name       text PRIMARY KEY,
    kind       text NOT NULL,
    -- Non-secret settings, shown by the API.
    config     jsonb NOT NULL DEFAULT '{}',
    -- AES-GCM encrypted JSON object of secret settings.
    secrets    bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE investigations (
    id            uuid PRIMARY KEY,
    title         text NOT NULL,
    source        text NOT NULL,
    -- Dedupe key from the alert source; open investigations with the same key are reused.
    fingerprint   text,
    services      text[] NOT NULL DEFAULT '{}',
    status        text NOT NULL,
    alert         jsonb NOT NULL DEFAULT '{}',
    report        jsonb,
    error         text,
    slack_channel text,
    slack_ts      text,
    input_tokens  bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);

CREATE INDEX investigations_created_at_idx ON investigations (created_at DESC);
CREATE INDEX investigations_fingerprint_idx ON investigations (fingerprint, created_at DESC);

-- Audit trail: every brief, tool call, LLM turn and report of an investigation.
CREATE TABLE steps (
    investigation_id uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
    seq              bigint NOT NULL,
    kind             text NOT NULL,
    name             text NOT NULL DEFAULT '',
    input            jsonb NOT NULL DEFAULT '{}',
    output           text NOT NULL DEFAULT '',
    is_error         boolean NOT NULL DEFAULT false,
    duration_ms      bigint NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (investigation_id, seq)
);

-- +goose Down
DROP TABLE steps;
DROP TABLE investigations;
DROP TABLE connectors;
DROP TABLE runbooks;
DROP TABLE services;
