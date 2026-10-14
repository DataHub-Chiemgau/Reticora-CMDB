-- Data integrity after a load or concurrency run (ABN-03, WP-207).
--
-- One row per check with the number of violations; every count must be 0.
-- The checks cover the [B] data model of G1: orphans, duplicates,
-- cross-tenant references and cycles of types, locations and the
-- composition. Negative stock belongs to the inventory module (P2, off in
-- G1 after E-03) and follows with it.
--
-- Run it as the owner (it reads every organization):
--   psql "$DATABASE_URL" -f tests/integrity-check.sql
-- or restricted to one organization:
--   psql "$DATABASE_URL" -c "SET integrity.org = '<uuid>'" -f tests/integrity-check.sql
-- backend/internal/ci/concurrency_integration_test.go runs it after the
-- concurrency matrix.
WITH RECURSIVE params AS (
    SELECT NULLIF(current_setting('integrity.org', true), '')::uuid AS org
),
scoped_ci AS (
    SELECT c.* FROM ci c, params p WHERE p.org IS NULL OR c.organization_id = p.org
),
scoped_rel AS (
    SELECT r.* FROM ci_relationship r, params p WHERE p.org IS NULL OR r.organization_id = p.org
),
type_walk AS (
    SELECT t.id AS start, t.cloned_from_id AS next, 1 AS depth
    FROM ci_type t, params p
    WHERE t.cloned_from_id IS NOT NULL AND (p.org IS NULL OR t.organization_id = p.org)
    UNION ALL
    SELECT w.start, t.cloned_from_id, w.depth + 1
    FROM type_walk w JOIN ci_type t ON t.id = w.next
    WHERE w.next <> w.start AND w.depth < 100
),
location_walk AS (
    SELECT l.id AS start, l.parent_id AS next, 1 AS depth
    FROM location l, params p
    WHERE l.parent_id IS NOT NULL AND (p.org IS NULL OR l.organization_id = p.org)
    UNION ALL
    SELECT w.start, l.parent_id, w.depth + 1
    FROM location_walk w JOIN location l ON l.id = w.next
    WHERE w.next <> w.start AND w.depth < 100
),
composition_walk AS (
    SELECT c.id AS start, c.parent_ci_id AS next, 1 AS depth
    FROM scoped_ci c WHERE c.parent_ci_id IS NOT NULL
    UNION ALL
    SELECT w.start, c.parent_ci_id, w.depth + 1
    FROM composition_walk w JOIN ci c ON c.id = w.next
    WHERE w.next <> w.start AND w.depth < 100
)
SELECT 'orphan: relationship to a deleted or merged CI' AS check_name, count(*) AS violations
FROM scoped_rel r
JOIN ci s ON s.id = r.source_ci_id
JOIN ci t ON t.id = r.target_ci_id
WHERE s.deleted_at IS NOT NULL OR t.deleted_at IS NOT NULL
UNION ALL
SELECT 'orphan: merged CI still active', count(*)
FROM scoped_ci WHERE merged_into_id IS NOT NULL AND deleted_at IS NULL
UNION ALL
SELECT 'orphan: rack mount of a deleted CI', count(*)
FROM rack_mount m JOIN scoped_ci c ON c.id = m.ci_id WHERE c.deleted_at IS NOT NULL
UNION ALL
SELECT 'duplicate: serial number of active CIs', count(*) FROM (
    SELECT organization_id, serial_number FROM scoped_ci
    WHERE deleted_at IS NULL AND serial_number IS NOT NULL AND serial_number <> ''
    GROUP BY 1, 2 HAVING count(*) > 1) d
UNION ALL
SELECT 'duplicate: hardware UUID of active CIs', count(*) FROM (
    SELECT organization_id, hardware_uuid FROM scoped_ci
    WHERE deleted_at IS NULL AND hardware_uuid IS NOT NULL
    GROUP BY 1, 2 HAVING count(*) > 1) d
UNION ALL
SELECT 'duplicate: primary MAC of active CIs', count(*) FROM (
    SELECT organization_id, primary_mac FROM scoped_ci
    WHERE deleted_at IS NULL AND primary_mac IS NOT NULL
    GROUP BY 1, 2 HAVING count(*) > 1) d
UNION ALL
SELECT 'cross-tenant: relationship endpoint', count(*)
FROM scoped_rel r
JOIN ci s ON s.id = r.source_ci_id
JOIN ci t ON t.id = r.target_ci_id
WHERE s.organization_id <> r.organization_id OR t.organization_id <> r.organization_id
UNION ALL
SELECT 'cross-tenant: rack mount', count(*)
FROM rack_mount m JOIN ci c ON c.id = m.ci_id JOIN rack k ON k.id = m.rack_id, params p
WHERE (p.org IS NULL OR m.organization_id = p.org)
  AND (c.organization_id <> m.organization_id OR k.organization_id <> m.organization_id)
UNION ALL
SELECT 'cross-tenant: network interface', count(*)
FROM network_interface n JOIN ci c ON c.id = n.ci_id, params p
WHERE (p.org IS NULL OR n.organization_id = p.org) AND c.organization_id <> n.organization_id
UNION ALL
SELECT 'cross-tenant: ip address', count(*)
FROM ip_address a JOIN network_interface n ON n.id = a.interface_id, params p
WHERE (p.org IS NULL OR a.organization_id = p.org) AND n.organization_id <> a.organization_id
UNION ALL
SELECT 'cross-tenant: field value', count(*)
FROM ci_field_value v JOIN ci c ON c.id = v.ci_id, params p
WHERE (p.org IS NULL OR v.organization_id = p.org) AND c.organization_id <> v.organization_id
UNION ALL
SELECT 'cross-tenant: CI client', count(*)
FROM scoped_ci c JOIN client k ON k.id = c.client_id WHERE k.organization_id <> c.organization_id
UNION ALL
SELECT 'cycle: CI type lineage', count(*) FROM type_walk WHERE next = start
UNION ALL
SELECT 'cycle: location tree', count(*) FROM location_walk WHERE next = start
UNION ALL
SELECT 'cycle: composition', count(*) FROM composition_walk WHERE next = start;
