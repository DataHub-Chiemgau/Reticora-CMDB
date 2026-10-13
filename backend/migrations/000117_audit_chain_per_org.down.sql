-- Migration 000117 down: back to the single chain of migration 000039.

DROP TRIGGER audit_log_chain ON audit_log;
DROP FUNCTION audit_log_chain_trigger();
ALTER TABLE audit_log DROP CONSTRAINT uq_audit_log_org_seq;
DROP FUNCTION audit_entry_hash(UUID, BIGINT, TIMESTAMPTZ, UUID, TEXT, TEXT, TEXT, UUID, JSONB, TEXT);

CREATE FUNCTION audit_entry_hash(
    p_timestamp TIMESTAMPTZ,
    p_actor_id UUID,
    p_actor_type TEXT,
    p_action TEXT,
    p_resource_type TEXT,
    p_resource_id UUID,
    p_changes JSONB,
    p_previous_hash TEXT
) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT encode(digest((
        '{"timestamp":' || to_jsonb(to_char(p_timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text)::text
        || ',"actor_id":' || to_jsonb(COALESCE(p_actor_id::text, '')::text)::text
        || ',"actor_type":' || to_jsonb(p_actor_type::text)::text
        || ',"action":' || to_jsonb(p_action::text)::text
        || ',"resource_type":' || to_jsonb(p_resource_type::text)::text
        || ',"resource_id":' || to_jsonb(COALESCE(p_resource_id::text, '')::text)::text
        || ',"changes":' || COALESCE(p_changes::text, '{}')
        || ',"previous_hash":' || to_jsonb(COALESCE(p_previous_hash, '')::text)::text
        || '}')::bytea, 'sha256'), 'hex');
$$;

-- Re-chain with the former formula: empty genesis, hash over JSON.
ALTER TABLE audit_log DISABLE TRIGGER trg_audit_log_no_update;
DO $$
DECLARE
    org RECORD;
    entry RECORD;
    prev TEXT;
BEGIN
    FOR org IN SELECT DISTINCT organization_id FROM audit_log LOOP
        prev := '';
        FOR entry IN
            SELECT id, timestamp, actor_id, actor_type, action, resource_type, resource_id, changes
            FROM audit_log
            WHERE organization_id = org.organization_id
            ORDER BY seq ASC
        LOOP
            UPDATE audit_log
            SET previous_hash = prev,
                hash = audit_entry_hash(entry.timestamp, entry.actor_id, entry.actor_type,
                                        entry.action, entry.resource_type, entry.resource_id,
                                        entry.changes, prev)
            WHERE id = entry.id
            RETURNING hash INTO prev;
        END LOOP;
    END LOOP;
END;
$$;
ALTER TABLE audit_log ENABLE TRIGGER trg_audit_log_no_update;

ALTER TABLE audit_log DROP COLUMN seq;
ALTER TABLE audit_log ALTER COLUMN previous_hash SET DEFAULT '';
DROP FUNCTION audit_org_lock_key(UUID);

CREATE FUNCTION audit_log_hash_trigger() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.hash := audit_entry_hash(NEW.timestamp, NEW.actor_id, NEW.actor_type,
                                 NEW.action, NEW.resource_type, NEW.resource_id,
                                 NEW.changes, NEW.previous_hash);
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_log_compute_hash
    BEFORE INSERT ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION audit_log_hash_trigger();
