ALTER TABLE work_tasks DROP COLUMN IF EXISTS task_preset;
ALTER TABLE work_tasks DROP COLUMN IF EXISTS assistant_instructions;
ALTER TABLE work_tasks DROP COLUMN IF EXISTS assistant_name;
ALTER TABLE work_execution_profiles DROP CONSTRAINT IF EXISTS assistant_task_presets_array;
ALTER TABLE work_execution_profiles DROP COLUMN IF EXISTS task_presets;
ALTER TABLE work_execution_profiles DROP COLUMN IF EXISTS instructions;
