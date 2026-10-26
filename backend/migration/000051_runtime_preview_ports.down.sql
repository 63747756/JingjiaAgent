CREATE TABLE runtime_preview_tickets (
    token_hash text PRIMARY KEY,
    forward_id uuid NOT NULL REFERENCES runtime_port_forwards(id),
    user_id uuid NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX runtime_preview_tickets_expiry ON runtime_preview_tickets(expires_at);
DROP TRIGGER runtime_preview_environment_deleted ON runtime_environments;
DROP FUNCTION release_runtime_preview_ports();
DROP INDEX runtime_preview_public_port;
ALTER TABLE runtime_port_forwards DROP COLUMN public_port, DROP COLUMN released_at;
