-- Only authenticated, server-observed employee addresses are stored here.
-- The nil task UUID is a creation-time fallback before the runtime task exists.
CREATE TABLE runtime_preview_client_ips (
    user_id uuid NOT NULL,
    task_id uuid NOT NULL,
    client_ip inet NOT NULL,
    observed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, task_id)
);
