-- Migration 000158 down: audit_entry_hash of migration 000118 (::bytea cast).
--
-- The chains are re-chained back the same way as on the way up: from the
-- first entry whose stored hash differs from the restored function, while
-- the chain is intact under the UTF-8 hash. An entry the restored function
-- cannot hash (a quote, LF, TAB or \uXXXX escape in its canonical JSON,
-- storable only since the up migration) ends the re-chain of its
-- organization; the restored function cannot verify such a chain.

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

-- Helpers of the re-chain as in the up migration, with the two digests
-- swapped.
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

-- The stored chains: UTF-8 (up migration).
CREATE FUNCTION audit_hash_former_digest(p_json TEXT) RETURNS BYTEA
LANGUAGE sql
STABLE
AS $$
    SELECT digest(convert_to(p_json, 'UTF8'), 'sha256');
$$;

-- The restored function: ::bytea; NULL where the cast raises 22P02.
CREATE FUNCTION audit_hash_digest(p_json TEXT) RETURNS BYTEA
LANGUAGE sql
STABLE
AS $$
    SELECT CASE WHEN strpos(replace(p_json, E'\\\\', ''), E'\\') = 0 THEN digest(p_json::bytea, 'sha256') END;
$$;

CREATE FUNCTION audit_hash_link(p_previous_hash TEXT, p_digest BYTEA) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT CASE WHEN p_previous_hash ~ '^([0-9a-fA-F]{2})*$'
                THEN encode(digest(decode(p_previous_hash, 'hex') || p_digest, 'sha256'), 'hex') END;
$$;

-- The same re-chain as in the up migration.
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
