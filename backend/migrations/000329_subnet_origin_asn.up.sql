-- Migration 000329: the origin AS of a prefix and the lowest free AS
-- number of a range (IPM-16, WP-1067).
--
-- 1. subnet.origin_asn_id: the AS number that originates the prefix, with a
--    composite foreign key on asn (Konvention 19); deleting the AS number
--    clears it. The IPAM change history (migration 000302) records it as a
--    field of the subnet like every other column.
-- 2. asn_next_free(org, lo, hi): the lowest AS number in [lo, hi] that no
--    AS number of the organization uses and that is not reserved (23456,
--    65535). It is set-based: the candidates are lo, every used number + 1
--    in the range and the number after a reserved one, each checked
--    against asn_number_key (organization_id, number) - no walk over the
--    whole range. The allocation must not hand out a number an AS number
--    of another client holds, so the search runs with the client, site and
--    team scope widened (app_scope_widen, 000217); the organization stays
--    fixed by app.org_id and the argument. It returns NULL for a full
--    range. The caller takes a transaction advisory lock per range
--    (pg_advisory_xact_lock on 'asn_range_alloc:' || id), so concurrent
--    allocations from one range take turns; a row lock (FOR UPDATE) is not
--    possible for a client principal, which lacks the UPDATE policy of an
--    organization-wide range.

ALTER TABLE subnet ADD COLUMN origin_asn_id uuid NULL;
ALTER TABLE subnet ADD CONSTRAINT subnet_origin_asn_fkey FOREIGN KEY (origin_asn_id, organization_id)
    REFERENCES asn (id, organization_id) ON DELETE SET NULL (origin_asn_id);
CREATE INDEX idx_subnet_origin_asn ON subnet (organization_id, origin_asn_id) WHERE origin_asn_id IS NOT NULL;

CREATE FUNCTION asn_next_free(org uuid, lo bigint, hi bigint) RETURNS bigint
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    found bigint;
BEGIN
    IF org IS DISTINCT FROM NULLIF(current_setting('app.org_id', true), '')::uuid THEN
        RETURN NULL;
    END IF;
    previous := app_scope_widen();
    SELECT min(c.n) INTO found
      FROM (SELECT lo AS n
            UNION ALL
            SELECT a.number + 1 FROM asn a
             WHERE a.organization_id = org AND a.number BETWEEN lo AND hi
            UNION ALL
            SELECT r.n + 1 FROM (VALUES (23456::bigint), (65535::bigint)) AS r(n)) c
     WHERE c.n BETWEEN lo AND hi
       AND c.n NOT IN (23456, 65535)
       AND NOT EXISTS (SELECT 1 FROM asn b WHERE b.organization_id = org AND b.number = c.n);
    PERFORM app_scope_restore(previous);
    RETURN found;
END
$$;
