-- Migration 000118 down: back to the audit fields of migration 000117.

DROP TRIGGER audit_log_chain ON audit_log;
DROP FUNCTION audit_log_chain_trigger();
DROP FUNCTION audit_entry_hash(UUID, BIGINT, TIMESTAMPTZ, UUID, TEXT, TEXT, TEXT, UUID, JSONB, TEXT, TEXT, TEXT, TEXT);

CREATE FUNCTION audit_entry_hash(
    p_organization_id UUID,
    p_seq BIGINT,
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
    SELECT encode(digest(decode(p_previous_hash, 'hex') || digest((
        '{"organization_id":' || to_jsonb(p_organization_id::text)::text
        || ',"seq":' || p_seq::text
        || ',"timestamp":' || to_jsonb(to_char(p_timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text)::text
        || ',"actor_id":' || to_jsonb(COALESCE(p_actor_id::text, '')::text)::text
        || ',"actor_type":' || to_jsonb(p_actor_type::text)::text
        || ',"action":' || to_jsonb(p_action::text)::text
        || ',"resource_type":' || to_jsonb(p_resource_type::text)::text
        || ',"resource_id":' || to_jsonb(COALESCE(p_resource_id::text, '')::text)::text
        || ',"changes":' || COALESCE(p_changes::text, '{}')
        || '}')::bytea, 'sha256'), 'sha256'), 'hex');
$$;

CREATE FUNCTION audit_log_chain_trigger() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    last_seq BIGINT;
    last_hash TEXT;
BEGIN
    PERFORM pg_advisory_xact_lock(1382380649, audit_org_lock_key(NEW.organization_id));
    SELECT seq, hash INTO last_seq, last_hash
    FROM audit_log
    WHERE organization_id = NEW.organization_id
    ORDER BY seq DESC
    LIMIT 1;
    NEW.seq := COALESCE(last_seq, 0) + 1;
    NEW.previous_hash := COALESCE(last_hash, repeat('0', 64));
    NEW.hash := audit_entry_hash(NEW.organization_id, NEW.seq, NEW.timestamp, NEW.actor_id, NEW.actor_type,
                                 NEW.action, NEW.resource_type, NEW.resource_id, NEW.changes, NEW.previous_hash);
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_log_chain
    BEFORE INSERT ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION audit_log_chain_trigger();

DROP INDEX idx_audit_request;
DROP INDEX idx_audit_correlation;
ALTER TABLE audit_log DROP COLUMN correlation_id;
ALTER TABLE audit_log DROP COLUMN request_id;
ALTER TABLE audit_log DROP COLUMN source;

ALTER TABLE audit_log DISABLE TRIGGER trg_audit_log_no_update;
DO $$
DECLARE
    org RECORD;
    entry RECORD;
    prev TEXT;
BEGIN
    FOR org IN SELECT DISTINCT organization_id FROM audit_log LOOP
        prev := repeat('0', 64);
        FOR entry IN SELECT * FROM audit_log WHERE organization_id = org.organization_id ORDER BY seq LOOP
            UPDATE audit_log
            SET previous_hash = prev,
                hash = audit_entry_hash(entry.organization_id, entry.seq, entry.timestamp, entry.actor_id, entry.actor_type, entry.action, entry.resource_type, entry.resource_id, entry.changes, prev)
            WHERE id = entry.id
            RETURNING hash INTO prev;
        END LOOP;
    END LOOP;
END;
$$;
ALTER TABLE audit_log ENABLE TRIGGER trg_audit_log_no_update;
