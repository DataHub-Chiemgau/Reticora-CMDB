-- Migration 000180: case-insensitive serial number lookup and the origin of
-- network interfaces (WP-580, REC-02, REC-07; F-246, F-249, E-90).
--
-- Identity resolution compares serial numbers case-insensitively, the
-- candidate search of the ingest compared serial_number case-sensitively:
-- 'AB-1' and 'ab-1' from different addresses became two CIs. The search now
-- compares lower(serial_number). lower() is not leakproof, so under the
-- row-level policies of the application role an index on the expression
-- would stay unused (see 000134): like hostname_lc the lower-case value is a
-- stored generated column compared with the leakproof text equality. The
-- index covers deleted CIs too (resurrection, CI-13).
--
-- REC-07 removes interfaces and addresses that three consecutive
-- observations missed. Only what discovery brought counts misses (E-90,
-- option 1): addresses of type discovered and interfaces of origin
-- discovered. Every existing interface becomes manual; the discovery sync
-- sets discovered on each interface it reports. network_interface stays a
-- tenant table with ENABLE + FORCE RLS, its policies are unchanged.

ALTER TABLE ci ADD COLUMN serial_number_lc TEXT GENERATED ALWAYS AS (lower(serial_number)) STORED;
CREATE INDEX IF NOT EXISTS idx_ci_serial_number_lc ON ci (organization_id, serial_number_lc) WHERE serial_number_lc IS NOT NULL;

ALTER TABLE network_interface
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual',
    ADD CONSTRAINT network_interface_origin_check CHECK (origin IN ('discovered', 'manual'));

-- The unmerge writes interfaces back from a merge snapshot (ci/merge.go
-- restoreRow): a snapshot taken before this migration has no origin, which
-- would restore NULL. Its interfaces become manual like every other.
UPDATE ci_merge m SET snapshot = m.snapshot || (
    SELECT jsonb_object_agg(part, COALESCE((
        SELECT jsonb_agg(CASE WHEN r->>'table' = 'network_interface' AND NOT (r->'row' ? 'origin')
            THEN jsonb_set(r, '{row,origin}', '"manual"') ELSE r END ORDER BY n)
        FROM jsonb_array_elements(m.snapshot->part) WITH ORDINALITY AS e(r, n)), '[]'::jsonb))
    FROM unnest(ARRAY['changed', 'removed']) AS part
    WHERE jsonb_typeof(m.snapshot->part) = 'array')
WHERE m.unmerged_at IS NULL
  AND (jsonb_path_exists(m.snapshot, '$.changed[*] ? (@.table == "network_interface")')
    OR jsonb_path_exists(m.snapshot, '$.removed[*] ? (@.table == "network_interface")'));
