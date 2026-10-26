-- A task outlives its individual runtime environments. Keep its address and
-- access settings until the task itself is deleted, including across restores.
-- No FK to tasks: hard deletion must retain the address during its cooldown.
ALTER TABLE runtime_port_forwards ADD COLUMN task_id uuid;

CREATE TEMP TABLE preview_environment_tasks ON COMMIT DROP AS
SELECT DISTINCT environment_id,task_id FROM (
    SELECT environment_id,task_id FROM runtime_task_intents
    UNION SELECT environment_id,task_id FROM runtime_commands WHERE task_id IS NOT NULL
) relationships;

-- Never guess ownership when historical runtime data is inconsistent.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM preview_environment_tasks r
        JOIN runtime_port_forwards f ON f.environment_id=r.environment_id
        GROUP BY r.environment_id HAVING count(DISTINCT r.task_id)>1) THEN
        RAISE EXCEPTION 'Preview migration: an environment belongs to multiple tasks; repair its runtime task relationships before retrying';
    END IF;
END $$;

CREATE TEMP TABLE ranked_task_previews ON COMMIT DROP AS
SELECT f.id,r.task_id,i.environment_id AS current_environment_id,
    (t.id IS NOT NULL AND t.deleted_at IS NULL) AS task_alive,
    row_number() OVER (PARTITION BY r.task_id,f.port
        ORDER BY (f.environment_id IS NOT DISTINCT FROM i.environment_id) DESC,
            f.created_at DESC,f.id DESC) AS preference
FROM runtime_port_forwards f
JOIN preview_environment_tasks r ON r.environment_id=f.environment_id
LEFT JOIN runtime_task_intents i ON i.task_id=r.task_id
LEFT JOIN tasks t ON t.id=r.task_id;

-- Keep the mapping currently visible to the employee, or the newest mapping
-- when the current environment has none. Retain superseded rows/whitelists for
-- audit, but close them and start cooldown. Do not reclaim an already reused
-- public port (a NULL public_port remains NULL until a new allocation).
UPDATE runtime_port_forwards f SET active=false,revision=f.revision+1,
    released_at=COALESCE(f.released_at,now())
FROM ranked_task_previews r WHERE r.id=f.id AND r.preference>1;

UPDATE runtime_port_forwards f SET task_id=r.task_id,
    environment_id=CASE WHEN r.task_alive THEN COALESCE(r.current_environment_id,f.environment_id) ELSE f.environment_id END,
    active=f.active AND r.task_alive,revision=f.revision+1,
    released_at=CASE WHEN r.task_alive THEN NULL ELSE COALESCE(f.released_at,now()) END
FROM ranked_task_previews r WHERE r.id=f.id AND r.preference=1;

CREATE UNIQUE INDEX runtime_preview_task_port ON runtime_port_forwards(task_id,port) WHERE task_id IS NOT NULL;

CREATE OR REPLACE FUNCTION release_runtime_preview_ports() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state = 'deleted' AND OLD.state IS DISTINCT FROM NEW.state THEN
        -- Disconnect current viewers, but preserve the task's access policy.
        UPDATE runtime_port_forwards SET revision=revision+1,
            active=CASE WHEN task_id IS NULL THEN false ELSE active END,
            released_at=CASE WHEN task_id IS NULL THEN COALESCE(released_at,now()) ELSE released_at END
        WHERE environment_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION release_task_preview_ports() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        UPDATE runtime_port_forwards SET active=false,revision=revision+1,
            released_at=COALESCE(released_at,now()) WHERE task_id=OLD.id;
        RETURN OLD;
    END IF;
    IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
        UPDATE runtime_port_forwards SET active=false,revision=revision+1,
            released_at=COALESCE(released_at,now()) WHERE task_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER runtime_preview_task_deleted AFTER UPDATE OF deleted_at OR DELETE ON tasks
    FOR EACH ROW EXECUTE FUNCTION release_task_preview_ports();
