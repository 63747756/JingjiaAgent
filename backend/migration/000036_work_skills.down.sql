DROP TABLE IF EXISTS work_skill_rollbacks;
DROP TABLE IF EXISTS work_skill_reviews;
DROP TABLE IF EXISTS work_skill_trials;
ALTER TABLE agent_skill_versions DROP COLUMN IF EXISTS content_hash;
ALTER TABLE agent_skills DROP CONSTRAINT IF EXISTS agent_skills_publication_state_check;
ALTER TABLE agent_skills DROP COLUMN IF EXISTS publication_state;
