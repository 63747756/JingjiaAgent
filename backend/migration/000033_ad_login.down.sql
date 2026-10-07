DROP INDEX unique_idx_users_email_role;
CREATE UNIQUE INDEX unique_idx_users_email_role ON users(lower(email),role)
 WHERE deleted_at IS NULL AND email IS NOT NULL AND email <> '';
DROP INDEX IF EXISTS user_identities_ad_identity;
DROP INDEX IF EXISTS team_group_members_identity;
DROP INDEX IF EXISTS team_groups_ad_identity;
ALTER TABLE team_group_members DROP COLUMN source;
ALTER TABLE team_groups DROP COLUMN last_synced_at, DROP COLUMN ou_path, DROP COLUMN external_dn, DROP COLUMN external_id, DROP COLUMN directory_id, DROP COLUMN source;
ALTER TABLE users DROP COLUMN login_name, DROP COLUMN auth_source;
DROP TABLE team_ad_configs;

ALTER TABLE team_groups ALTER COLUMN name TYPE varchar(255);

ALTER TABLE users ALTER COLUMN name TYPE varchar(255);
