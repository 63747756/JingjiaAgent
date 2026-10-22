ALTER TABLE work_execution_profiles ADD COLUMN instructions text NOT NULL DEFAULT '';
ALTER TABLE work_execution_profiles ADD COLUMN task_presets jsonb NOT NULL DEFAULT '[]'::jsonb;

-- Default buttons are ordinary editable records, without execution categories.
UPDATE work_execution_profiles SET task_presets = (
    SELECT coalesce(jsonb_agg(jsonb_build_object(
        'id', preset.id, 'name', preset.name, 'description', preset.description,
        'icon', preset.icon, 'enabled', true) ORDER BY preset.position), '[]'::jsonb)
    FROM (VALUES
        ('general', '通用任务', '根据用户目标和提供的资料完成工作，交付可核验的成果。', 'sparkles', 1),
        ('data', '数据分析', '分析上传的表格，说明数据口径、关键指标和异常，并生成可复核的分析报告。', 'chart-column', 2),
        ('document', '文档报告', '根据提供的资料整理一份报告，保留来源依据，按结论、分析和建议组织内容。', 'file-text', 3),
        ('presentation', '演示文稿', '根据提供的资料制作一份汇报 PPT，包含关键结论、图表和行动建议。', 'presentation', 4)
    ) AS preset(id, name, description, icon, position)
);

ALTER TABLE work_execution_profiles ADD CONSTRAINT assistant_task_presets_array CHECK (jsonb_typeof(task_presets) = 'array');
ALTER TABLE work_tasks ADD COLUMN assistant_name text NOT NULL DEFAULT '';
ALTER TABLE work_tasks ADD COLUMN assistant_instructions text NOT NULL DEFAULT '';
ALTER TABLE work_tasks ADD COLUMN task_preset jsonb;

-- Do not inject new instructions into historical tasks.
UPDATE work_tasks w SET assistant_name=p.name FROM work_execution_profiles p WHERE p.id=w.profile_id;
