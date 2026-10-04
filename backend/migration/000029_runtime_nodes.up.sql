CREATE TABLE runtime_nodes (
    node_id text PRIMARY KEY,
    instance_id text UNIQUE,
    fingerprint text,
    observation jsonb,
    ready boolean NOT NULL DEFAULT false,
    last_seen_at timestamptz,
    last_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error_code text NOT NULL DEFAULT ''
);
