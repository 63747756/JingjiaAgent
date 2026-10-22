CREATE TABLE work_resources (
 id uuid PRIMARY KEY, owner_id uuid NOT NULL REFERENCES users(id), project_id uuid REFERENCES projects(id),
 filename text NOT NULL, current_version_id uuid, created_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz
);
CREATE TABLE work_resource_versions (
 id uuid PRIMARY KEY, resource_id uuid NOT NULL REFERENCES work_resources(id), filename text NOT NULL,
 storage_key text NOT NULL UNIQUE, sha256 text NOT NULL, size bigint NOT NULL CHECK(size>=0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE work_artifacts (
 id uuid PRIMARY KEY, owner_id uuid NOT NULL REFERENCES users(id), task_id uuid NOT NULL REFERENCES tasks(id),
 project_id uuid REFERENCES projects(id), title text NOT NULL, filename text NOT NULL, source_path text NOT NULL,
 current_version_id uuid, created_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz,
 UNIQUE(task_id,source_path)
);
CREATE TABLE work_artifact_versions (
 id uuid PRIMARY KEY, artifact_id uuid NOT NULL REFERENCES work_artifacts(id), version integer NOT NULL,
 command_id uuid, source_version_id uuid REFERENCES work_artifact_versions(id), filename text NOT NULL, mime_type text NOT NULL, storage_key text NOT NULL UNIQUE,
 sha256 text NOT NULL, size bigint NOT NULL CHECK(size>=0), preview_key text NOT NULL DEFAULT '',
 validation_status text NOT NULL DEFAULT 'pending', preview_status text NOT NULL DEFAULT 'pending',
 validation jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(artifact_id,version), UNIQUE(artifact_id,command_id)
);
CREATE TABLE work_artifact_publications (
 artifact_id uuid NOT NULL REFERENCES work_artifacts(id), version_id uuid NOT NULL REFERENCES work_artifact_versions(id),
 project_id uuid NOT NULL REFERENCES projects(id), published_by uuid NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(artifact_id,version_id,project_id)
);
CREATE TABLE workspace_checkpoints (
 id uuid PRIMARY KEY, task_id uuid NOT NULL REFERENCES tasks(id), environment_id text NOT NULL REFERENCES runtime_environments(id),
 generation integer NOT NULL DEFAULT 1, command_id uuid, state text NOT NULL DEFAULT 'pending',
 archive_key text NOT NULL DEFAULT '', payload_sha256 text NOT NULL DEFAULT '', manifest jsonb NOT NULL DEFAULT '{}'::jsonb,
 size bigint NOT NULL DEFAULT 0, error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(),
 committed_at timestamptz, UNIQUE(environment_id,command_id)
);
CREATE TABLE work_storage_jobs (
 id uuid PRIMARY KEY, task_id uuid NOT NULL REFERENCES tasks(id), environment_id text NOT NULL REFERENCES runtime_environments(id),
 command_id uuid, kind text NOT NULL DEFAULT 'checkpoint', state text NOT NULL DEFAULT 'pending',
 request jsonb NOT NULL DEFAULT '{}'::jsonb, attempts integer NOT NULL DEFAULT 0,
 lease_token uuid, lease_until timestamptz, available_at timestamptz NOT NULL DEFAULT now(),
 error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(environment_id,command_id,kind)
);
CREATE INDEX work_storage_jobs_ready ON work_storage_jobs(available_at) WHERE state IN ('pending','running','failed');
CREATE INDEX work_resources_owner ON work_resources(owner_id,created_at DESC);
CREATE INDEX work_artifacts_owner ON work_artifacts(owner_id,created_at DESC);
ALTER TABLE runtime_environments ADD COLUMN generation integer NOT NULL DEFAULT 1;
ALTER TABLE runtime_environments ADD COLUMN checkpoint_id uuid REFERENCES workspace_checkpoints(id);
ALTER TABLE runtime_environments ADD COLUMN archiving_checkpoint_id uuid;
CREATE TABLE runtime_restores (
 task_id uuid PRIMARY KEY REFERENCES tasks(id), old_environment_id text NOT NULL, new_environment_id text NOT NULL REFERENCES runtime_environments(id),
 checkpoint_id uuid NOT NULL REFERENCES workspace_checkpoints(id), archive_key text NOT NULL, payload_sha256 text NOT NULL,
 state text NOT NULL DEFAULT 'preparing', created_at timestamptz NOT NULL DEFAULT now()
);
