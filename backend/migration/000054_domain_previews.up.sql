-- Keep the route UUID and task/port identity. Routes no longer reserve public
-- ports: all previews share a domain gateway, and stopped services may reuse
-- the same route when the task's workspace is restored.
ALTER TABLE runtime_port_forwards
    ADD COLUMN access_mode text NOT NULL DEFAULT 'ip_allowlist'
        CHECK (access_mode IN ('public', 'ip_allowlist'));

-- Preserve every existing policy, including an empty deny-all whitelist. Only
-- routes created after this migration default to anonymous public access.
ALTER TABLE runtime_port_forwards
    ALTER COLUMN access_mode SET DEFAULT 'public',
    ALTER COLUMN whitelist SET DEFAULT '[]'::jsonb;

CREATE OR REPLACE FUNCTION release_runtime_preview_ports() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state = 'deleted' AND OLD.state IS DISTINCT FROM NEW.state THEN
        UPDATE runtime_port_forwards SET revision=revision+1,
            active=CASE WHEN task_id IS NULL THEN false ELSE active END
        WHERE environment_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION release_task_preview_ports() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        UPDATE runtime_port_forwards SET active=false,revision=revision+1 WHERE task_id=OLD.id;
        RETURN OLD;
    END IF;
    IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
        UPDATE runtime_port_forwards SET active=false,revision=revision+1 WHERE task_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$;

DROP INDEX runtime_preview_public_port;
ALTER TABLE runtime_port_forwards DROP COLUMN public_port, DROP COLUMN released_at;

-- An access-policy edit now reads the current trusted connection's IP. There
-- is no per-user observed-IP fallback shared across browser connections.
DROP TABLE runtime_preview_client_ips;
