-- Internal admission journal; no external calls may precede the atomic admission.
CREATE TABLE runtime_creation_attempts (
    vm_id text PRIMARY KEY,
    owner_id uuid NOT NULL,
    task_id uuid NOT NULL UNIQUE,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','admitted','failed','uncertain')),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '10 minutes',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runtime_creation_attempts_pending ON runtime_creation_attempts(expires_at) WHERE state='pending';
