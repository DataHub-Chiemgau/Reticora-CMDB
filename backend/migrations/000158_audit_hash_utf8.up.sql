-- Migration 000158: audit_entry_hash hashes the canonical JSON as UTF-8
-- (AUD-04, WP-423).
--
-- Since 000117 the function cast the canonical JSON text with ::bytea. The
-- cast reads backslash sequences as bytea escapes: the JSON escapes of a
-- quote, LF, TAB or \uXXXX raised 22P02 and rolled back the audited write
-- (a CI with such a value could be neither stored nor changed), and an
-- escaped backslash collapsed to one byte, so the hash was not SHA-256 of
-- the canonical JSON (F-045). The function now hashes
-- convert_to(<canonical JSON>, 'UTF8') with the same signature, field order
-- and canonicalization; the trigger audit_log_chain calls it by name.
--
-- Only entries with a backslash in their canonical JSON hash differently.
-- Each organization's chain is re-chained in seq order and in batches from
-- its first entry whose stored hash differs from the new one; chains without
-- such an entry keep their hashes. An entry is re-chained only while the
-- chain up to it is intact under the former hash (sequence, link and hash):
-- a tampered or broken entry ends the re-chain of its organization and stays
-- detectable. The first remaining entry of a purged chain keeps its anchor
-- (audit.retention). The append-only guard is lifted only inside this
-- migration. Run time: docs/ops/workers.md.

-- Appends wait until the chains are re-chained.
LOCK TABLE audit_log IN SHARE ROW EXCLUSIVE MODE;

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

-- Helpers of the re-chain, dropped below. They are single expressions the
-- planner inlines, so the chains are hashed set-based.
--
-- audit_hash_json is the canonical JSON audit_entry_hash hashes.
CREATE FUNCTION audit_hash_json(
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
    p_correlation_id TEXT
) RETURNS TEXT
LANGUAGE sql
STABLE
AS $$
    SELECT '{"organization_id":' || to_jsonb(p_organization_id::text)::text
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
        || '}';
$$;

-- audit_hash_former_digest is SHA-256 of the canonical JSON as the stored
-- chains hashed it (::bytea, migration 000118). The cast accepts a JSON text
-- only if no backslash is left once the escaped backslashes (\\) are taken
-- out; otherwise it raised 22P02 and never chained the entry (NULL).
CREATE FUNCTION audit_hash_former_digest(p_json TEXT) RETURNS BYTEA
LANGUAGE sql
STABLE
AS $$
    SELECT CASE WHEN strpos(replace(p_json, E'\\\\', ''), E'\\') = 0 THEN digest(p_json::bytea, 'sha256') END;
$$;

-- audit_hash_digest is SHA-256 of the canonical JSON as audit_entry_hash
-- hashes it from now on.
CREATE FUNCTION audit_hash_digest(p_json TEXT) RETURNS BYTEA
LANGUAGE sql
STABLE
AS $$
    SELECT digest(convert_to(p_json, 'UTF8'), 'sha256');
$$;

-- audit_hash_link is SHA-256(previous_hash || digest), hex-encoded; NULL for
-- a previous hash that is not hex.
CREATE FUNCTION audit_hash_link(p_previous_hash TEXT, p_digest BYTEA) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT CASE WHEN p_previous_hash ~ '^([0-9a-fA-F]{2})*$'
                THEN encode(digest(decode(p_previous_hash, 'hex') || p_digest, 'sha256'), 'hex') END;
$$;

-- Current statistics let the batches below read their seq window through
-- the index on (organization_id, seq), also for a chain written since the
-- last analyze.
ANALYZE audit_log;
ALTER TABLE audit_log DISABLE TRIGGER trg_audit_log_no_update;
DO $$
DECLARE
    org RECORD;
    entry RECORD;
    expected BIGINT;
    stored_prev TEXT;
    prev TEXT;
    next_hash TEXT;
    intact BOOLEAN;
    ids UUID[];
    prevs TEXT[];
    hashes TEXT[];
BEGIN
    FOR org IN
        SELECT organization_id, min(seq) AS first_seq
        FROM (SELECT organization_id, seq, previous_hash, hash,
                     audit_hash_json(organization_id, seq, timestamp, actor_id, actor_type, action, resource_type,
                                     resource_id, changes, source, request_id, correlation_id) AS canonical
              FROM audit_log
              OFFSET 0) AS a
        WHERE hash IS DISTINCT FROM audit_hash_link(previous_hash, audit_hash_digest(canonical))
        GROUP BY organization_id
    LOOP
        -- The entry before the first differing one keeps its hash; without
        -- one, the first remaining entry keeps its genesis or anchor.
        SELECT seq, hash INTO expected, stored_prev
        FROM audit_log
        WHERE organization_id = org.organization_id AND seq < org.first_seq
        ORDER BY seq DESC
        LIMIT 1;
        expected := COALESCE(expected + 1, org.first_seq);
        prev := stored_prev;
        intact := true;
        WHILE intact LOOP
            ids := '{}';
            prevs := '{}';
            hashes := '{}';
            FOR entry IN
                SELECT id, seq, previous_hash, hash,
                       audit_hash_link(previous_hash, audit_hash_former_digest(canonical)) AS former_hash,
                       audit_hash_digest(canonical) AS digest
                FROM (SELECT id, seq, previous_hash, hash,
                             audit_hash_json(organization_id, seq, timestamp, actor_id, actor_type, action,
                                             resource_type, resource_id, changes, source, request_id,
                                             correlation_id) AS canonical
                      FROM audit_log
                      WHERE organization_id = org.organization_id AND seq >= expected AND seq < expected + 1000
                      OFFSET 0) AS a
                ORDER BY seq
            LOOP
                IF entry.seq <> expected
                   OR entry.previous_hash IS DISTINCT FROM COALESCE(stored_prev, entry.previous_hash)
                   OR entry.hash IS DISTINCT FROM entry.former_hash THEN
                    intact := false;
                    EXIT;
                END IF;
                prev := COALESCE(prev, entry.previous_hash);
                next_hash := audit_hash_link(prev, entry.digest);
                IF next_hash IS NULL THEN
                    intact := false;
                    EXIT;
                END IF;
                ids := ids || entry.id;
                prevs := prevs || prev;
                hashes := hashes || next_hash;
                stored_prev := entry.hash;
                prev := next_hash;
                expected := entry.seq + 1;
            END LOOP;
            UPDATE audit_log AS a
            SET previous_hash = u.previous_hash, hash = u.hash
            FROM unnest(ids, prevs, hashes) AS u(id, previous_hash, hash)
            WHERE a.id = u.id;
            EXIT WHEN cardinality(ids) < 1000;
        END LOOP;
    END LOOP;
END;
$$;
ALTER TABLE audit_log ENABLE TRIGGER trg_audit_log_no_update;

DROP FUNCTION audit_hash_link(TEXT, BYTEA);
DROP FUNCTION audit_hash_digest(TEXT);
DROP FUNCTION audit_hash_former_digest(TEXT);
DROP FUNCTION audit_hash_json(UUID, BIGINT, TIMESTAMPTZ, UUID, TEXT, TEXT, TEXT, UUID, JSONB, TEXT, TEXT, TEXT);
