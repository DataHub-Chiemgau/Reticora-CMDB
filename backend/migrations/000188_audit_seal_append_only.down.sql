-- Migration 000188 down: the state of 000125 (WP-241). Entries sealed since
-- 000188 are re-chained from the first sealed entry of their organization,
-- as the backfill did then, because the restored verification hashes the
-- present changes.

DROP FUNCTION IF EXISTS audit_seal_pii(UUID, JSONB);

CREATE OR REPLACE FUNCTION audit_entry_hash(
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
    SELECT encode(digest(decode(p_previous_hash, 'hex') || digest(convert_to(
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
        || '}', 'UTF8'), 'sha256'), 'sha256'), 'hex');
$$;

CREATE OR REPLACE FUNCTION reject_audit_log_mutation() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.audit_retention', true) = 'on'
       AND OLD.timestamp < now() - interval '10 years' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE'
       AND current_setting('app.audit_pii_migration', true) = 'on'
       AND (NEW.id, NEW.organization_id, NEW.seq, NEW.timestamp, NEW.actor_id, NEW.actor_type, NEW.action,
            NEW.resource_type, NEW.resource_id, NEW.source, NEW.request_id, NEW.correlation_id, NEW.created_at)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.organization_id, OLD.seq, OLD.timestamp, OLD.actor_id, OLD.actor_type, OLD.action,
            OLD.resource_type, OLD.resource_id, OLD.source, OLD.request_id, OLD.correlation_id, OLD.created_at) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'audit_log rows are append-only';
END;
$$ LANGUAGE plpgsql;

-- audit_rechain recomputes previous_hash and hash of an organization's
-- entries from p_from_seq on, after their changes were sealed; it returns
-- how many entries it re-chained. The caller holds the chain lock.
CREATE FUNCTION audit_rechain(p_org UUID, p_from_seq BIGINT) RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    prev TEXT;
    entry RECORD;
    n INTEGER := 0;
BEGIN
    IF current_setting('app.audit_pii_migration', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'audit_rechain runs only in the pii migration';
    END IF;
    SELECT hash INTO prev FROM audit_log WHERE organization_id = p_org AND seq < p_from_seq ORDER BY seq DESC LIMIT 1;
    IF NOT FOUND THEN
        -- The first entry of a purged chain keeps its anchor.
        SELECT previous_hash INTO prev FROM audit_log WHERE organization_id = p_org ORDER BY seq LIMIT 1;
    END IF;
    FOR entry IN SELECT * FROM audit_log WHERE organization_id = p_org AND seq >= p_from_seq ORDER BY seq LOOP
        UPDATE audit_log
        SET previous_hash = prev,
            hash = audit_entry_hash(entry.organization_id, entry.seq, entry.timestamp, entry.actor_id, entry.actor_type,
                entry.action, entry.resource_type, entry.resource_id, entry.changes, entry.source, entry.request_id,
                entry.correlation_id, prev)
        WHERE id = entry.id
        RETURNING hash INTO prev;
        n := n + 1;
    END LOOP;
    RETURN n;
END;
$$;

DO $$
DECLARE
    org RECORD;
BEGIN
    PERFORM set_config('app.audit_pii_migration', 'on', true);
    FOR org IN SELECT organization_id, min(seq) AS from_seq FROM audit_log WHERE chained_digest IS NOT NULL
               GROUP BY organization_id LOOP
        PERFORM pg_advisory_xact_lock(1382380649, audit_org_lock_key(org.organization_id));
        PERFORM audit_rechain(org.organization_id, org.from_seq);
    END LOOP;
    PERFORM set_config('app.audit_pii_migration', 'off', true);
END
$$;

CREATE OR REPLACE FUNCTION audit_log_chain_trigger() RETURNS trigger
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

ALTER TABLE audit_log DROP COLUMN sealed_hash;
ALTER TABLE audit_log DROP COLUMN sealed_refs;
ALTER TABLE audit_log DROP COLUMN chained_digest;

DROP FUNCTION IF EXISTS audit_unseal(JSONB, JSONB);
DROP FUNCTION IF EXISTS audit_changes_seal(JSONB, JSONB, UUID, BOOLEAN);
DROP FUNCTION IF EXISTS audit_personal_keys();
DROP FUNCTION IF EXISTS audit_chain_link(TEXT, TEXT);
DROP FUNCTION IF EXISTS audit_entry_digest(UUID, BIGINT, TIMESTAMPTZ, UUID, TEXT, TEXT, TEXT, UUID, JSONB, TEXT, TEXT, TEXT);

GRANT UPDATE ON audit_log TO reticora_app;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reticora_audit_sealer') THEN
        DROP OWNED BY reticora_audit_sealer;
        BEGIN
            DROP ROLE reticora_audit_sealer;
        EXCEPTION
            WHEN dependent_objects_still_exist THEN NULL; -- still used by another database
        END;
    END IF;
END
$$;
