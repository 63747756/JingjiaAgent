DROP TABLE work_restore_objects;
DROP INDEX workspace_checkpoints_retention;
ALTER TABLE workspace_checkpoints DROP COLUMN retention_checked_at;
UPDATE work_storage_jobs SET kind='checkpoint',request=request-'artifacts_saved' WHERE kind='artifacts';
