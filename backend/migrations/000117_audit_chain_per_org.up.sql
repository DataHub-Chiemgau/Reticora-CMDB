-- Migration 000117: audit hash chain per organization with a sequence
-- (AUD-04, WP-132).
--
-- Every organization has its own chain: seq numbers its entries without
-- gaps (1, 2, 3, ...), the first entry links to 32 zero bytes, and
--   hash = SHA-256(prev_hash || SHA-256(canonical JSON))
-- over the binary values (hex-encoded in the columns). The canonical JSON
-- has a fixed key order and carries organization_id and seq, so an entry
-- cannot move to another chain or position unnoticed.
--
-- The BEFORE INSERT trigger owns the chain: under the organization's
-- transaction advisory lock (the key the application already takes,
-- audit.AcquireAdvisoryLock) it reads the last entry and sets seq,
-- previous_hash and hash. The lock is held until commit, so concurrent
-- writers append one after the other in commit order and cannot fork the
-- chain; UNIQUE (organization_id, seq) rejects a fork outright.

-- The lock key of an organization: the first four bytes of SHA-256 of its
-- id as a signed integer (audit.AcquireAdvisoryLock).
CREATE FUNCTION audit_org_lock_key(p_organization_id UUID) RETURNS INTEGER
LANGUAGE sql IMMUTABLE
AS $$
    SELECT ('x' || substr(encode(digest(p_organization_id::text, 'sha256'), 'hex'), 1, 8))::bit(32)::integer;
$$;

DROP TRIGGER audit_log_compute_hash ON audit_log;
DROP FUNCTION audit_log_hash_trigger();
DROP FUNCTION audit_entry_hash(TIMESTAMPTZ, UUID, TEXT, TEXT, TEXT, UUID, JSONB, TEXT);

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

ALTER TABLE audit_log ADD COLUMN seq BIGINT;
ALTER TABLE audit_log ALTER COLUMN previous_hash SET DEFAULT repeat('0', 64);

-- Re-chain the existing entries per organization in their former order.
ALTER TABLE audit_log DISABLE TRIGGER trg_audit_log_no_update;
DO $$
DECLARE
    org RECORD;
    entry RECORD;
    prev TEXT;
    n BIGINT;
BEGIN
    FOR org IN SELECT DISTINCT organization_id FROM audit_log LOOP
        prev := repeat('0', 64);
        n := 0;
        FOR entry IN
            SELECT id, timestamp, actor_id, actor_type, action, resource_type, resource_id, changes
            FROM audit_log
            WHERE organization_id = org.organization_id
            ORDER BY timestamp ASC, id ASC
        LOOP
            n := n + 1;
            UPDATE audit_log
            SET seq = n,
                previous_hash = prev,
                hash = audit_entry_hash(org.organization_id, n, entry.timestamp, entry.actor_id, entry.actor_type,
                                        entry.action, entry.resource_type, entry.resource_id, entry.changes, prev)
            WHERE id = entry.id
            RETURNING hash INTO prev;
        END LOOP;
    END LOOP;
END;
$$;
ALTER TABLE audit_log ENABLE TRIGGER trg_audit_log_no_update;

ALTER TABLE audit_log ALTER COLUMN seq SET NOT NULL;
ALTER TABLE audit_log ADD CONSTRAINT uq_audit_log_org_seq UNIQUE (organization_id, seq);

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
