DROP TRIGGER runtime_preview_task_deleted ON tasks;
DROP FUNCTION release_task_preview_ports();
DROP INDEX runtime_preview_task_port;
ALTER TABLE runtime_port_forwards DROP COLUMN task_id;
CREATE OR REPLACE FUNCTION release_runtime_preview_ports() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state = 'deleted' AND OLD.state IS DISTINCT FROM NEW.state THEN
        UPDATE runtime_port_forwards SET active=false,revision=revision+1,
            released_at=COALESCE(released_at,now()) WHERE environment_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$;
UPDATE runtime_port_forwards f SET active=false,revision=f.revision+1,released_at=COALESCE(f.released_at,now())
FROM runtime_environments e WHERE f.environment_id=e.id AND e.state='deleted';
