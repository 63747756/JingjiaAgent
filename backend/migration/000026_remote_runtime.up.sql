CREATE TABLE runtime_environments (
    id text PRIMARY KEY,
    owner_id uuid NOT NULL,
    node_id text NOT NULL,
    backend text NOT NULL CHECK (backend = 'agent_compose'),
    project_id text NOT NULL DEFAULT '',
    sandbox_id text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending',
    payload bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE runtime_commands (
    id uuid PRIMARY KEY,
    environment_id text NOT NULL REFERENCES runtime_environments(id),
    task_id uuid,
    operation text NOT NULL,
    turn integer NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT 'pending',
    payload bytea NOT NULL,
    payload_hash text NOT NULL,
    run_id text NOT NULL DEFAULT '',
    submission_started boolean NOT NULL DEFAULT false,
    cancel_requested boolean NOT NULL DEFAULT false,
    event_offset bigint NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0,
    lease_token uuid,
    lease_until timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (task_id, operation, turn)
);
CREATE INDEX runtime_commands_ready ON runtime_commands (available_at, created_at)
    WHERE state IN ('pending', 'submitting', 'unknown', 'running');
CREATE TABLE runtime_task_intents (
    task_id uuid PRIMARY KEY,
    environment_id text NOT NULL REFERENCES runtime_environments(id),
    payload bytea NOT NULL,
    payload_hash text NOT NULL,
    cancel_requested boolean NOT NULL DEFAULT false
);
CREATE TABLE runtime_events (
    seq bigserial PRIMARY KEY,
    task_id uuid NOT NULL,
    command_id uuid NOT NULL REFERENCES runtime_commands(id),
    source_key text NOT NULL,
    turn integer NOT NULL,
    chunk jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (command_id, source_key)
);
CREATE INDEX runtime_events_task ON runtime_events (task_id, seq);
