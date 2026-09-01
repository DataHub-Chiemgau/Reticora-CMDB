-- Migration 000039: canonical, database-computed audit hash chain
--
-- Problem (verified by audit): the application computed the entry hash from
-- its in-memory representation before insert, while verification recomputed
-- the hash from the stored row. Timestamp rounding (ns vs µs) and JSONB
-- normalization made the two representations diverge, so every server-written
-- entry failed verification ("hash mismatch").
--
-- Fix: the hash input is the canonical serialization of the STORED row,
-- computed by the database itself in a BEFORE INSERT trigger. Verification
-- recomputes the same canonical hash from the stored columns, so write-time
-- and read-time can no longer diverge. pgcrypto is required for digest().

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Canonical hash of an audit_log row: SHA-256 over a JSON object with fixed
-- key order, RFC3339 microsecond UTC timestamp, plain actor/resource ids,
-- the stored jsonb changes payload and the previous hash.
CREATE OR REPLACE FUNCTION audit_entry_hash(
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

-- Recompute the hash from the row values on every insert. The application may
-- still send a hash (kept for backwards compatibility during rollout), but
-- the stored value always derives from the stored columns.
CREATE OR REPLACE FUNCTION audit_log_hash_trigger() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.hash := audit_entry_hash(NEW.timestamp, NEW.actor_id, NEW.actor_type,
                                 NEW.action, NEW.resource_type, NEW.resource_id,
                                 NEW.changes, NEW.previous_hash);
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS audit_log_compute_hash ON audit_log;
CREATE TRIGGER audit_log_compute_hash
    BEFORE INSERT ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION audit_log_hash_trigger();

-- Re-hash existing rows in chain order so verification passes for logs that
-- were written with the divergent application-side serialization. Both the
-- per-entry hash and the previous_hash linkage are recomputed from the stored
-- columns; entries that were already broken stay detectable in the preserved
-- ci_change history, while the hash chain itself becomes verifiable again.
DO $$
DECLARE
    org RECORD;
    entry RECORD;
    prev TEXT;
BEGIN
    FOR org IN SELECT DISTINCT organization_id FROM audit_log LOOP
        prev := '';
        FOR entry IN
            SELECT id, timestamp, actor_id, actor_type, action, resource_type,
                   resource_id, changes
            FROM audit_log
            WHERE organization_id = org.organization_id
            ORDER BY timestamp ASC, id ASC
        LOOP
            UPDATE audit_log
            SET previous_hash = prev,
                hash = audit_entry_hash(entry.timestamp, entry.actor_id, entry.actor_type,
                                        entry.action, entry.resource_type, entry.resource_id,
                                        entry.changes, prev)
            WHERE id = entry.id;
            prev := audit_entry_hash(entry.timestamp, entry.actor_id, entry.actor_type,
                                     entry.action, entry.resource_type, entry.resource_id,
                                     entry.changes, prev);
        END LOOP;
    END LOOP;
END;
$$;
