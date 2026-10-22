DROP TRIGGER guard_task_continuation ON tasks;
DROP TRIGGER guard_work_task ON work_tasks;
DROP TRIGGER guard_project_task ON project_tasks;
DROP FUNCTION guard_task_project();
ALTER TABLE runtime_task_intents DROP COLUMN deletion_requested;
ALTER TABLE work_tasks DROP COLUMN retained_resource_versions;
DROP TABLE navigation_pins;
