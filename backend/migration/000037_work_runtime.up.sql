CREATE TABLE work_run_slots (
    command_id uuid PRIMARY KEY REFERENCES runtime_commands(id),
    task_id uuid NOT NULL UNIQUE REFERENCES work_tasks(task_id),
    profile_id uuid NOT NULL REFERENCES work_execution_profiles(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
