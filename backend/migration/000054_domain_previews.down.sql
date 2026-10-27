-- Recreate the previous schema for a code rollback. Discarded external-port
-- assignments cannot be restored; the old allocator must assign new ports.
ALTER TABLE runtime_port_forwards
    ADD COLUMN public_port integer CHECK (public_port BETWEEN 1024 AND 65535),
    ADD COLUMN released_at timestamptz;
CREATE UNIQUE INDEX runtime_preview_public_port ON runtime_port_forwards(public_port) WHERE public_port IS NOT NULL;

CREATE OR REPLACE FUNCTION release_runtime_preview_ports() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state = 'deleted' AND OLD.state IS DISTINCT FROM NEW.state THEN
        UPDATE runtime_port_forwards SET revision=revision+1,
            active=CASE WHEN task_id IS NULL THEN false ELSE active END,
            released_at=CASE WHEN task_id IS NULL THEN COALESCE(released_at,now()) ELSE released_at END
        WHERE environment_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION release_task_preview_ports() RETURNS trigger LANGUAGE plpgsql AS $$
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

UPDATE runtime_port_forwards f SET active=false,revision=f.revision+1,released_at=now()
WHERE (f.task_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id=f.task_id AND t.deleted_at IS NULL))
   OR (f.task_id IS NULL AND EXISTS (SELECT 1 FROM runtime_environments e WHERE e.id=f.environment_id AND e.state='deleted'));

ALTER TABLE runtime_port_forwards DROP COLUMN access_mode, ALTER COLUMN whitelist DROP DEFAULT;
CREATE TABLE runtime_preview_client_ips (
    user_id uuid NOT NULL,
    task_id uuid NOT NULL,
    client_ip inet NOT NULL,
    observed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, task_id)
);
