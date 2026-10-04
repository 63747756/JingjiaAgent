ALTER TABLE runtime_nodes ADD COLUMN capacity_id text NOT NULL DEFAULT '';

CREATE TABLE runtime_capacity_pools (
    id text PRIMARY KEY,
    cpu_total_millis bigint NOT NULL CHECK (cpu_total_millis > 0),
    memory_total_bytes bigint NOT NULL CHECK (memory_total_bytes > 0),
    cpu_limit_millis bigint NOT NULL CHECK (cpu_limit_millis >= 0),
    memory_limit_bytes bigint NOT NULL CHECK (memory_limit_bytes >= 0),
    enforced boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Environment tombstones are permanent. No FK to runtime_environments: node
-- enrollment backfills reservations while holding a pool lock, and must not
-- acquire environment locks in the reverse order of normal admission.
CREATE TABLE runtime_reservations (
    environment_id text PRIMARY KEY,
    capacity_id text NOT NULL REFERENCES runtime_capacity_pools(id),
    cpu_millis bigint NOT NULL CHECK (cpu_millis > 0),
    memory_bytes bigint NOT NULL CHECK (memory_bytes > 0),
    active boolean NOT NULL DEFAULT true,
    release_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runtime_reservations_active_pool ON runtime_reservations(capacity_id) WHERE active;
CREATE INDEX runtime_nodes_capacity ON runtime_nodes(capacity_id) WHERE capacity_id <> '';
