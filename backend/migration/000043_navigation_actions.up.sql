CREATE TABLE navigation_pins (
 user_id uuid NOT NULL REFERENCES users(id), kind text NOT NULL CHECK(kind IN ('task','project')),
 target_id uuid NOT NULL, pinned_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,kind,target_id)
);
ALTER TABLE work_tasks ADD COLUMN retained_resource_versions jsonb NOT NULL DEFAULT '[]';
ALTER TABLE runtime_task_intents ADD COLUMN deletion_requested boolean NOT NULL DEFAULT false;

-- Serialize task creation/continuation with project removal. A deleted project
-- cannot acquire a new task even if the request passed its earlier access check.
CREATE FUNCTION guard_task_project() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE pid uuid; gone timestamptz;
BEGIN
 IF TG_TABLE_NAME = 'tasks' THEN
  IF NEW.status NOT IN ('pending','processing') THEN RETURN NEW; END IF;
  SELECT project_id INTO pid FROM project_tasks WHERE task_id=NEW.id LIMIT 1;
 ELSE
  pid := NEW.project_id;
 END IF;
 IF pid IS NOT NULL THEN
  SELECT deleted_at INTO gone FROM projects WHERE id=pid FOR UPDATE;
  IF NOT FOUND OR gone IS NOT NULL THEN
   RAISE EXCEPTION '项目已删除，请刷新页面后重试';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_project_task BEFORE INSERT OR UPDATE OF project_id ON project_tasks FOR EACH ROW EXECUTE FUNCTION guard_task_project();
CREATE TRIGGER guard_work_task BEFORE INSERT OR UPDATE OF project_id ON work_tasks FOR EACH ROW EXECUTE FUNCTION guard_task_project();
CREATE TRIGGER guard_task_continuation BEFORE UPDATE OF status ON tasks FOR EACH ROW EXECUTE FUNCTION guard_task_project();
