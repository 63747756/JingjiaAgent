ALTER TABLE runtime_port_forwards
    ADD COLUMN public_port integer CHECK (public_port BETWEEN 1024 AND 65535),
    ADD COLUMN released_at timestamptz;
CREATE UNIQUE INDEX runtime_preview_public_port ON runtime_port_forwards(public_port) WHERE public_port IS NOT NULL;

-- Closing a preview does not release its address. Only deleting its environment
-- starts the 24-hour cooldown; allocation later reclaims expired reservations.
CREATE FUNCTION release_runtime_preview_ports() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state = 'deleted' AND OLD.state IS DISTINCT FROM NEW.state THEN
        UPDATE runtime_port_forwards SET active=false, revision=revision+1,
            released_at=COALESCE(released_at,now()) WHERE environment_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER runtime_preview_environment_deleted AFTER UPDATE OF state ON runtime_environments
    FOR EACH ROW EXECUTE FUNCTION release_runtime_preview_ports();
UPDATE runtime_port_forwards f SET active=false, released_at=now()
    FROM runtime_environments e WHERE f.environment_id=e.id AND e.state='deleted';

-- Preview viewing is anonymous and IP-restricted; admission tickets are obsolete.
DROP TABLE runtime_preview_tickets;
