CREATE TABLE team_ad_configs (
    id uuid PRIMARY KEY NOT NULL,
    team_id uuid NOT NULL UNIQUE REFERENCES teams(id) ON DELETE CASCADE,
    directory_id uuid NOT NULL UNIQUE,
    enabled boolean NOT NULL DEFAULT false,
    display_name text NOT NULL DEFAULT 'AD 域登录',
    url text NOT NULL DEFAULT '', base_dn text NOT NULL DEFAULT '', bind_dn text NOT NULL DEFAULT '',
    bind_password_ciphertext text NOT NULL DEFAULT '', ca_pem text NOT NULL DEFAULT '',
    allowed_group_dns jsonb NOT NULL DEFAULT '[]', revision integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX team_ad_configs_single_enabled ON team_ad_configs(enabled) WHERE enabled = true;
ALTER TABLE users ADD COLUMN auth_source text NOT NULL DEFAULT 'local';
ALTER TABLE users ADD COLUMN login_name text NOT NULL DEFAULT '';
ALTER TABLE team_groups ADD COLUMN source text NOT NULL DEFAULT 'manual';
ALTER TABLE team_groups ADD COLUMN directory_id uuid;
ALTER TABLE team_groups ADD COLUMN external_id text NOT NULL DEFAULT '';
ALTER TABLE team_groups ADD COLUMN external_dn text NOT NULL DEFAULT '';
ALTER TABLE team_groups ADD COLUMN ou_path text NOT NULL DEFAULT '';
ALTER TABLE team_groups ADD COLUMN last_synced_at timestamptz;
ALTER TABLE team_group_members ADD COLUMN source text NOT NULL DEFAULT 'manual';
CREATE UNIQUE INDEX team_groups_ad_identity ON team_groups(team_id,directory_id,external_id) WHERE source = 'ad_ou' AND deleted_at IS NULL;
CREATE UNIQUE INDEX team_group_members_identity ON team_group_members(group_id,user_id);
CREATE UNIQUE INDEX user_identities_ad_identity ON user_identities(platform,identity_id) WHERE platform = 'ad';

ALTER TABLE team_groups ALTER COLUMN name TYPE text;

DROP INDEX unique_idx_users_email_role;
CREATE UNIQUE INDEX unique_idx_users_email_role ON users(lower(email),role)
 WHERE deleted_at IS NULL AND email IS NOT NULL AND email <> '' AND auth_source <> 'ad';

ALTER TABLE users ALTER COLUMN name TYPE text;
