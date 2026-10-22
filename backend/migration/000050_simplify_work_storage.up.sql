ALTER TABLE workspace_checkpoints ADD COLUMN retention_checked_at timestamptz NOT NULL DEFAULT '-infinity';
CREATE INDEX workspace_checkpoints_retention ON workspace_checkpoints(retention_checked_at) WHERE state IN ('committed','pruning');
CREATE TABLE work_restore_objects (
 archive_key text PRIMARY KEY,
 checkpoint_id uuid NOT NULL REFERENCES workspace_checkpoints(id),
 created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO work_restore_objects(archive_key,checkpoint_id)
 SELECT archive_key,checkpoint_id FROM runtime_restores WHERE archive_key LIKE 'work-private/restores/%'
 ON CONFLICT DO NOTHING;
-- Already committed legacy saves have extracted their artifacts. Finish their
-- lifecycle/conversion without exporting another partial archive after upgrade.
UPDATE work_storage_jobs j SET kind='artifacts',request=request || jsonb_build_object('scope','artifacts','artifacts_saved',EXISTS(SELECT 1 FROM workspace_checkpoints cp WHERE cp.id=j.id AND cp.state='committed'))
 WHERE kind='checkpoint' AND COALESCE(request->>'scope','')<>'workspace';
