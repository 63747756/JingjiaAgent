CREATE TABLE runtime_port_forwards (
    id uuid PRIMARY KEY,
    environment_id text NOT NULL REFERENCES runtime_environments(id),
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    whitelist jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(environment_id, port)
);
CREATE TABLE runtime_preview_tickets (
    token_hash text PRIMARY KEY,
    forward_id uuid NOT NULL REFERENCES runtime_port_forwards(id),
    user_id uuid NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX runtime_preview_tickets_expiry ON runtime_preview_tickets(expires_at);
