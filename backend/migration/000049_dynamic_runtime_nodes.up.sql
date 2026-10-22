CREATE TABLE runtime_node_configs (
    node_id text PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users(id),
    team_id uuid REFERENCES teams(id),
    name text NOT NULL DEFAULT '',
    endpoint text NOT NULL,
    payload bytea NOT NULL,
    accepting boolean NOT NULL DEFAULT true,
    phase text NOT NULL DEFAULT 'waiting' CHECK (phase IN ('waiting','verifying','ready')),
    imported boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX runtime_node_active_endpoint ON runtime_node_configs(endpoint) WHERE revoked_at IS NULL;
CREATE INDEX runtime_node_owner ON runtime_node_configs(owner_id,team_id);
CREATE FUNCTION notify_runtime_node_config() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('jingjiaagent_runtime_nodes', NEW.node_id);
    RETURN NEW;
END $$;
CREATE TRIGGER runtime_node_config_changed AFTER INSERT OR UPDATE ON runtime_node_configs
FOR EACH ROW EXECUTE FUNCTION notify_runtime_node_config();

-- Existing host-delete APIs must observe the same fence as the new UI.
CREATE FUNCTION guard_runtime_host_removal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' AND (NEW.deleted_at IS NULL OR OLD.deleted_at IS NOT NULL) THEN RETURN NEW; END IF;
    IF EXISTS(SELECT 1 FROM runtime_node_configs WHERE node_id=OLD.id) THEN
        PERFORM pg_advisory_xact_lock(707326030);
        IF EXISTS(SELECT 1 FROM runtime_environments WHERE node_id=OLD.id AND state<>'deleted') THEN
            RAISE EXCEPTION '宿主机仍有关联工作空间，请先停止接收新任务并处理关联任务';
        END IF;
        UPDATE runtime_node_configs SET accepting=false, revoked_at=COALESCE(revoked_at,now()),
            revision=revision+1, updated_at=now() WHERE node_id=OLD.id;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER runtime_host_remove_guard BEFORE DELETE OR UPDATE OF deleted_at ON hosts
FOR EACH ROW EXECUTE FUNCTION guard_runtime_host_removal();
