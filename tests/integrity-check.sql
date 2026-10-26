-- Data integrity after a load or concurrency run (ABN-03, WP-207, WP-437).
--
-- One row per check with the number of violations; every count must be 0.
-- The checks cover the [B] data model of G1: orphans, duplicates,
-- cross-tenant references and cycles of types, locations and the
-- composition. Negative stock belongs to the inventory module (P2, off in
-- G1 after E-03) and follows with it.
--
-- Only what the schema does not already prevent is checked (F-281): the
-- exact duplicates of serial number, hardware UUID and primary MAC among
-- the active CIs of an organization are refused by the unique indexes
-- uq_ci_org_serial, uq_ci_org_hardware_uuid and uq_ci_org_primary_mac. The
-- identity resolution (REC-02) compares more loosely: serial numbers
-- without case and surrounding blanks, and devices without a strong
-- identity (no serial number, hardware UUID or MAC) by management IP and
-- sysObjectID in one VRF, or by hostname/FQDN, which never merges but
-- opens a probable_duplicate review. Foreign keys without the organization
-- let a CI point at the location, site, room or custom type of another
-- organization, and a rack at another organization's room.
--
-- Run it as the owner (it reads every organization):
--   psql "$DATABASE_URL" -f tests/integrity-check.sql
-- or restricted to one organization:
--   psql "$DATABASE_URL" -c "SET integrity.org = '<uuid>'" -f tests/integrity-check.sql
-- .github/workflows/load.yml runs it after every load job; the concurrency
-- tests of backend/internal/ci and backend/internal/discovery and the seed
-- test run it after their runs. backend/internal/ci/integrity_check_integration_test.go
-- injects one violation per check and expects it to be counted.
WITH RECURSIVE params AS (
    SELECT NULLIF(current_setting('integrity.org', true), '')::uuid AS org
),
scoped_ci AS (
    SELECT c.* FROM ci c, params p WHERE p.org IS NULL OR c.organization_id = p.org
),
scoped_rel AS (
    SELECT r.* FROM ci_relationship r, params p WHERE p.org IS NULL OR r.organization_id = p.org
),
-- Active CIs without a strong identity, with the VRF their management IP
-- belongs to (the record's VRF, otherwise the default VRF; REC-02 step 4).
weak_ci AS (
    SELECT c.*, COALESCE(NULLIF(c.attributes #>> '{raw_data,vrf_id}', ''),
        (SELECT v.id::text FROM vrf v WHERE v.organization_id = c.organization_id AND v.is_default LIMIT 1), '') AS vrf
    FROM scoped_ci c
    WHERE c.deleted_at IS NULL AND btrim(COALESCE(c.serial_number, '')) = '' AND c.hardware_uuid IS NULL
      AND c.primary_mac IS NULL AND COALESCE(c.attributes #>> '{fingerprint,hardware_uuid}', '') = ''
      AND COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(c.attributes #> '{fingerprint,mac_addresses}') = 'array'
          THEN c.attributes #> '{fingerprint,mac_addresses}' END), 0) = 0
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
    SELECT organization_id, upper(btrim(serial_number)) FROM scoped_ci
    WHERE deleted_at IS NULL AND btrim(COALESCE(serial_number, '')) <> ''
    GROUP BY 1, 2 HAVING count(*) > 1) d
UNION ALL
SELECT 'duplicate: management IP and sysObjectID of weak identities in one VRF', count(*) FROM (
    SELECT organization_id, vrf, host(management_ip), lower(btrim(sys_object_id)) FROM weak_ci
    WHERE management_ip IS NOT NULL AND btrim(COALESCE(sys_object_id, '')) <> ''
    GROUP BY 1, 2, 3, 4 HAVING count(*) > 1) d
UNION ALL
SELECT 'duplicate: hostname or FQDN of weak identities without review', count(*)
FROM weak_ci a
JOIN weak_ci b ON b.organization_id = a.organization_id AND b.id > a.id
    AND ((a.hostname_lc <> '' AND a.hostname_lc = b.hostname_lc) OR (a.fqdn_lc <> '' AND a.fqdn_lc = b.fqdn_lc))
WHERE NOT EXISTS (
    SELECT 1 FROM review_item r
    WHERE r.organization_id = a.organization_id AND r.kind = 'probable_duplicate'
      AND ((r.payload->>'ci_id' = a.id::text AND b.id = ANY (r.candidate_ci_ids))
        OR (r.payload->>'ci_id' = b.id::text AND a.id = ANY (r.candidate_ci_ids))))
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
SELECT 'cross-tenant: CI location', count(*)
FROM scoped_ci c JOIN location l ON l.id = c.location_id WHERE l.organization_id <> c.organization_id
UNION ALL
SELECT 'cross-tenant: CI site', count(*)
FROM scoped_ci c JOIN site s ON s.id = c.site_id WHERE s.organization_id <> c.organization_id
UNION ALL
SELECT 'cross-tenant: CI room', count(*)
FROM scoped_ci c JOIN room r ON r.id = c.room_id WHERE r.organization_id <> c.organization_id
UNION ALL
SELECT 'cross-tenant: CI type', count(*)
FROM scoped_ci c JOIN ci_type t ON t.id = c.ci_type_id WHERE t.organization_id <> c.organization_id
UNION ALL
SELECT 'cross-tenant: rack room', count(*)
FROM rack k JOIN room r ON r.id = k.room_id, params p
WHERE (p.org IS NULL OR k.organization_id = p.org) AND r.organization_id <> k.organization_id
UNION ALL
SELECT 'cycle: CI type lineage', count(*) FROM type_walk WHERE next = start
UNION ALL
SELECT 'cycle: location tree', count(*) FROM location_walk WHERE next = start
UNION ALL
SELECT 'cycle: composition', count(*) FROM composition_walk WHERE next = start;
