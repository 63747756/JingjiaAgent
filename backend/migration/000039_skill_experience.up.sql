ALTER TABLE projects ADD COLUMN IF NOT EXISTS default_profile_id uuid;
CREATE TABLE IF NOT EXISTS work_skillhub_bindings (
 user_id uuid PRIMARY KEY REFERENCES users(id), registry_url text NOT NULL,
 remote_user_id text NOT NULL, remote_name text NOT NULL DEFAULT '',
 token_ciphertext text NOT NULL, token_id bigint, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS work_skillhub_installs (
 user_id uuid NOT NULL REFERENCES users(id), registry_url text NOT NULL,
 namespace text NOT NULL, slug text NOT NULL, skill_id uuid NOT NULL REFERENCES agent_skills(id),
 remote_version text NOT NULL, package_hash text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,registry_url,namespace,slug)
);
CREATE TABLE IF NOT EXISTS work_skillhub_uploads (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id), skill_id uuid NOT NULL REFERENCES agent_skills(id),
 version_id uuid NOT NULL REFERENCES agent_skill_versions(id), registry_url text NOT NULL,
 namespace text NOT NULL, slug text NOT NULL, remote_version text NOT NULL,
 package_hash text NOT NULL, status text NOT NULL, result jsonb NOT NULL DEFAULT '{}',
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,skill_id,registry_url,namespace,remote_version)
);
