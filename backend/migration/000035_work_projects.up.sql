ALTER TABLE projects ADD COLUMN IF NOT EXISTS project_kind varchar(16) NOT NULL DEFAULT 'code';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS rules text NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS default_skill_ids jsonb NOT NULL DEFAULT '[]';
ALTER TABLE projects ADD CONSTRAINT projects_work_kind_check CHECK (project_kind IN ('business','code'));
CREATE INDEX IF NOT EXISTS projects_work_team_idx ON projects(team_id) WHERE deleted_at IS NULL;

CREATE TABLE work_execution_profiles (
    id uuid PRIMARY KEY,
    team_id uuid NOT NULL REFERENCES teams(id),
    name varchar(120) NOT NULL,
    description text NOT NULL DEFAULT '',
    model_id uuid NOT NULL,
    host_id text NOT NULL,
    image_id uuid NOT NULL,
    cli_name varchar(16) NOT NULL DEFAULT 'opencode',
    required_skill_ids jsonb NOT NULL DEFAULT '[]',
    core integer NOT NULL DEFAULT 2 CHECK(core BETWEEN 1 AND 32),
    memory bigint NOT NULL DEFAULT 8589934592 CHECK(memory BETWEEN 1073741824 AND 137438953472),
    life bigint NOT NULL DEFAULT 10800 CHECK(life BETWEEN 300 AND 86400),
    max_concurrent_tasks integer NOT NULL DEFAULT 5 CHECK(max_concurrent_tasks BETWEEN 1 AND 30),
    enabled boolean NOT NULL DEFAULT true,
    is_default boolean NOT NULL DEFAULT false,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(team_id,name),
    CHECK(cli_name IN ('opencode','codex','claude'))
);
CREATE UNIQUE INDEX work_profile_default_idx ON work_execution_profiles(team_id) WHERE is_default AND enabled;

CREATE TABLE work_tasks (
    task_id uuid PRIMARY KEY REFERENCES tasks(id),
    owner_id uuid NOT NULL REFERENCES users(id),
    team_id uuid NOT NULL REFERENCES teams(id),
    project_id uuid REFERENCES projects(id),
    profile_id uuid NOT NULL REFERENCES work_execution_profiles(id),
    resource_manifest jsonb NOT NULL DEFAULT '[]',
    skill_manifest jsonb NOT NULL DEFAULT '[]',
    output_requirements text NOT NULL DEFAULT '',
    project_rules text NOT NULL DEFAULT '',
	client_request_id uuid,
	request_payload_hash text NOT NULL DEFAULT '',
    delivery_status varchar(24) NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK(delivery_status IN ('pending','validating','delivered','failed','waiting_confirmation'))
);
CREATE INDEX work_tasks_owner_idx ON work_tasks(owner_id,created_at DESC);
CREATE INDEX work_tasks_project_idx ON work_tasks(project_id,created_at DESC);
CREATE UNIQUE INDEX work_tasks_client_request_idx ON work_tasks(owner_id,client_request_id) WHERE client_request_id IS NOT NULL;
