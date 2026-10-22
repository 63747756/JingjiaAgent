ALTER TABLE work_tasks ADD COLUMN project_id uuid REFERENCES projects(id);
UPDATE work_tasks w SET project_id=pt.project_id FROM project_tasks pt WHERE pt.task_id=w.task_id;
CREATE INDEX work_tasks_project_idx ON work_tasks(project_id,created_at DESC);
CREATE TRIGGER guard_work_task BEFORE INSERT OR UPDATE OF project_id ON work_tasks
 FOR EACH ROW EXECUTE FUNCTION guard_task_project();
ALTER TABLE project_tasks DROP CONSTRAINT project_tasks_project_id_valid;
ALTER TABLE project_tasks DROP CONSTRAINT project_tasks_task_id_unique;
