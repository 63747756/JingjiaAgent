DROP TABLE IF EXISTS work_tasks;
DROP TABLE IF EXISTS work_execution_profiles;
DROP INDEX IF EXISTS projects_work_team_idx;
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_work_kind_check;
ALTER TABLE projects DROP COLUMN IF EXISTS default_skill_ids;
ALTER TABLE projects DROP COLUMN IF EXISTS rules;
ALTER TABLE projects DROP COLUMN IF EXISTS team_id;
ALTER TABLE projects DROP COLUMN IF EXISTS project_kind;
