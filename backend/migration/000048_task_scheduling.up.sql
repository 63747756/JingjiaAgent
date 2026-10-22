ALTER TABLE teams ADD COLUMN task_total_concurrency_limit integer NOT NULL DEFAULT 5 CHECK (task_total_concurrency_limit > 0);
ALTER TABLE work_execution_profiles DROP COLUMN life, DROP COLUMN max_concurrent_tasks;
ALTER TABLE work_run_slots DROP CONSTRAINT work_run_slots_task_id_fkey;
ALTER TABLE work_run_slots ADD CONSTRAINT work_run_slots_task_id_fkey FOREIGN KEY(task_id) REFERENCES tasks(id);
ALTER TABLE work_run_slots DROP COLUMN profile_id;
ALTER TABLE work_run_slots ADD COLUMN owner_id uuid, ADD COLUMN team_id uuid;
UPDATE work_run_slots s SET owner_id=t.user_id,team_id=COALESCE(w.team_id,
 (SELECT m.team_id FROM team_members m JOIN teams tm ON tm.id=m.team_id WHERE m.user_id=t.user_id AND tm.deleted_at IS NULL ORDER BY tm.created_at,tm.id LIMIT 1))
 FROM tasks t LEFT JOIN work_tasks w ON w.task_id=t.id WHERE s.task_id=t.id;
CREATE INDEX work_run_slots_team_owner ON work_run_slots(team_id,owner_id);
ALTER TABLE runtime_commands ADD COLUMN queue_reason text NOT NULL DEFAULT '';
-- In-flight legacy runs must count immediately after upgrade. Preparation
-- slots already present are retained and transfer to the first prompt round.
INSERT INTO work_run_slots(command_id,task_id,owner_id,team_id)
SELECT DISTINCT ON(c.task_id) c.id,c.task_id,t.user_id,COALESCE(w.team_id,
 (SELECT m.team_id FROM team_members m JOIN teams tm ON tm.id=m.team_id WHERE m.user_id=t.user_id AND tm.deleted_at IS NULL ORDER BY tm.created_at,tm.id LIMIT 1))
FROM runtime_commands c JOIN tasks t ON t.id=c.task_id LEFT JOIN work_tasks w ON w.task_id=t.id
WHERE c.operation IN ('task','prepare','restore_prepare') AND c.submission_started
 AND c.state IN ('pending','submitting','unknown','running')
ORDER BY c.task_id,c.created_at DESC ON CONFLICT DO NOTHING;
