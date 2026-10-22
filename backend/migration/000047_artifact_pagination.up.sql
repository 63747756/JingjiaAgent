CREATE INDEX work_artifacts_page ON work_artifacts(created_at DESC,id DESC) WHERE deleted_at IS NULL;
CREATE INDEX work_artifacts_task_page ON work_artifacts(task_id,created_at DESC,id DESC) WHERE deleted_at IS NULL;
