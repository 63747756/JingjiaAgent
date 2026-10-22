-- project_tasks is the single owner of a task's project association.
-- Do not guess an association when upgrading inconsistent data: that could
-- accidentally grant access to another project's files.
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM project_tasks GROUP BY task_id HAVING count(*) > 1) THEN
  RAISE EXCEPTION '任务项目关联存在重复记录，请先核对 project_tasks';
 END IF;
 IF EXISTS (SELECT 1 FROM work_tasks w LEFT JOIN project_tasks pt ON pt.task_id=w.task_id
            WHERE pt.task_id IS NULL OR w.project_id IS DISTINCT FROM pt.project_id) THEN
  RAISE EXCEPTION '任务项目关联不一致，请先核对 work_tasks 与 project_tasks';
 END IF;
END $$;

ALTER TABLE project_tasks ADD CONSTRAINT project_tasks_task_id_unique UNIQUE(task_id);
ALTER TABLE project_tasks ADD CONSTRAINT project_tasks_project_id_valid
 CHECK (project_id IS NULL OR project_id <> '00000000-0000-0000-0000-000000000000'::uuid);
DROP TRIGGER guard_work_task ON work_tasks;
ALTER TABLE work_tasks DROP COLUMN project_id;

-- guard_project_task and guard_task_continuation already use project_tasks;
-- both continue to serialize admission/continuation with project deletion.
