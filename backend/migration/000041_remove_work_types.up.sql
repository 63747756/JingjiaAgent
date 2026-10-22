-- Remove obsolete fields from development databases created before the cleanup.
ALTER TABLE work_execution_profiles DROP COLUMN IF EXISTS scenarios;
ALTER TABLE work_tasks DROP COLUMN IF EXISTS scenario;

UPDATE work_execution_profiles p SET task_presets = (
    SELECT coalesce(jsonb_agg((item - 'scenario') ||
        jsonb_build_object('icon', coalesce(item->>'icon', 'sparkles')) ORDER BY position), '[]'::jsonb)
    FROM jsonb_array_elements(p.task_presets) WITH ORDINALITY AS presets(item, position)
);
UPDATE work_tasks SET task_preset = (task_preset - 'scenario') ||
    jsonb_build_object('icon', coalesce(task_preset->>'icon', 'sparkles'))
    WHERE task_preset IS NOT NULL;
