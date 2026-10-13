-- Migration 000118: audit fields of AUD-07 (WP-133).
--
-- Every audit entry records where the change came from (source: ui, api,
-- collector, import, system), the request that made it (request_id) and
-- the correlation id that ties the entries of one business operation
-- together across requests and jobs. The three fields enter the canonical
-- JSON of the hash chain (AUD-04), so they cannot be altered unnoticed;
-- existing entries are re-chained per organization in seq order.

ALTER TABLE audit_log ADD COLUMN source TEXT NOT NULL DEFAULT 'system'
    CONSTRAINT audit_log_source_check CHECK (source IN ('ui', 'api', 'collector', 'import', 'system'));
ALTER TABLE audit_log ADD COLUMN request_id TEXT;
ALTER TABLE audit_log ADD COLUMN correlation_id TEXT;

CREATE INDEX idx_audit_correlation ON audit_log (organization_id, correlation_id) WHERE correlation_id IS NOT NULL;
CREATE INDEX idx_audit_request ON audit_log (organization_id, request_id) WHERE request_id IS NOT NULL;

DROP TRIGGER audit_log_chain ON audit_log;
DROP FUNCTION audit_log_chain_trigger();
DROP FUNCTION audit_entry_hash(UUID, BIGINT, TIMESTAMPTZ, UUID, TEXT, TEXT, TEXT, UUID, JSONB, TEXT);

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
    p_source TEXT,
    p_request_id TEXT,
    p_correlation_id TEXT,
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
        || ',"source":' || to_jsonb(p_source::text)::text
        || ',"request_id":' || to_jsonb(COALESCE(p_request_id, '')::text)::text
        || ',"correlation_id":' || to_jsonb(COALESCE(p_correlation_id, '')::text)::text
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
                                 NEW.action, NEW.resource_type, NEW.resource_id, NEW.changes,
                                 NEW.source, NEW.request_id, NEW.correlation_id, NEW.previous_hash);
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_log_chain
    BEFORE INSERT ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION audit_log_chain_trigger();

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
                hash = audit_entry_hash(entry.organization_id, entry.seq, entry.timestamp, entry.actor_id, entry.actor_type, entry.action, entry.resource_type, entry.resource_id, entry.changes, entry.source, entry.request_id, entry.correlation_id, prev)
            WHERE id = entry.id
            RETURNING hash INTO prev;
        END LOOP;
    END LOOP;
END;
$$;
ALTER TABLE audit_log ENABLE TRIGGER trg_audit_log_no_update;
