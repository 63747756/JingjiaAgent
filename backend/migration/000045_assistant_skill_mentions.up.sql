ALTER TABLE work_execution_profiles
    ADD COLUMN mentionable_skill_ids jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(mentionable_skill_ids) = 'array');
