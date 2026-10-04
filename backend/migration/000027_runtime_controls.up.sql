ALTER TABLE runtime_commands ADD COLUMN result jsonb;
CREATE TABLE runtime_task_sessions (
    task_id uuid PRIMARY KEY REFERENCES runtime_task_intents(task_id),
    provider text NOT NULL,
    session_id text NOT NULL DEFAULT '',
    command_id uuid NOT NULL REFERENCES runtime_commands(id),
    updated_at timestamptz NOT NULL DEFAULT now()
);
