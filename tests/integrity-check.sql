-- Data integrity after a load or concurrency run (ABN-03, WP-207, WP-437,
-- WP-328).
--
-- One row per check with the number of violations; every count must be 0.
-- The checks cover the [B] data model of G1: orphans, duplicates,
-- cross-tenant references and cycles of types, locations and the
-- composition. Phase 2 adds stock, reservation and assignment (WP-328):
-- negative stock and reserved below zero or above on_hand (stock_level;
-- the CHECKs of WP-283 refuse both, the rows are the explicit proof of
-- ABN-03), reserved other than the sum of the active quantity reservations
-- of the stock row (WP-284), an active asset reservation whose asset is
-- held by someone other than the reservation's user (WP-277), a holder
-- cache (asset.assigned_user_id, assigned_team_id; WP-275) other than the
-- holder the movements assignment and return of the asset give (a holder
-- change books return and assignment at the same occurred_at; pure
-- location moves, transfer, do not count; WP-274), cycles of the type
-- inheritance (ci_type.parent_type_id, WP-248) and the normalized serial
-- number of more than one asset of an organization that is not disposed
-- (lower(btrim()) as review_serial_mismatch, 000088; AST-03).
-- The cross-tenant references of phase 2 (WP-827) are checked where no
-- composite foreign key with the organization guards them: the document of
-- a document link (document_link_document_id_fkey has no organization) and
-- its target (entity_type, entity_id: no foreign key; a target no table
-- holds is an orphan), the values of the reference-typed and file
-- attributes in ci.attributes (reference, ci_ref, asset_ref, contract_ref,
-- user, team, location, file; definitions from attribute_definition; a
-- value no table holds is an orphan), the asset type (ci_type holds global
-- rows, so asset_asset_type_id_fkey has no organization) and, in one row,
-- every other single-column foreign key between two tenant tables without
-- the organization (rls.ReferencesWithoutOrganization, WP-838, among them
-- ticket.team_id, ticket.assignee_id, document.uploaded_by and
-- stocktake.started_by). The context of a stock movement (ticket_id,
-- order_id, document_id, workflow_run_id) has no foreign key: the ledger is
-- append-only and keeps the id of a deleted ticket or order, so only a
-- context of another organization counts.
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
-- organization, and a rack at another organization's room. The schema
-- itself guarantees, so the script does not check: one active reservation
-- per asset (idx_reservation_one_active_per_asset); inventory number, RFID
-- and barcode per organization (idx_asset_tag_org, idx_asset_rfid,
-- idx_asset_barcode_org, WP-271); consumable.sku and consumable.barcode
-- per organization (WP-283); one stock row per (consumable_id,
-- location_id) (stock_level); no own asset of a configuration child (CHECK
-- ci_configuration_child_asset, WP-273); the references with a composite
-- foreign key that carries the organization (among others
-- ci_asset_id_fkey, 000088; since WP-838 and the P2 migrations the
-- references of reservation to asset and, for quantity reservations,
-- through (consumable_id, location_id, organization_id) to stock_level
-- (WP-284), of stock_level to consumable and location (WP-283), of
-- stock_movement to asset, consumable, locations, user and team, of asset
-- to supplier, contract, client, location, site, holder, owner and CI, of
-- ticket_link, internal_order, internal_order_item, stocktake and
-- stocktake_item to their objects).
--
-- Run it as the owner (it reads every organization):
--   psql "$DATABASE_URL" -f tests/integrity-check.sql
-- or restricted to one organization:
--   psql "$DATABASE_URL" -c "SET integrity.org = '<uuid>'" -f tests/integrity-check.sql
-- .github/workflows/load.yml runs it after every load job; the concurrency
-- tests of backend/internal/ci, backend/internal/discovery and
-- backend/internal/tenant/integrity and the seed test run it after their
-- runs through scopetest.IntegrityViolations
-- (backend/internal/database/scopetest/integrity.go).
-- backend/internal/ci/integrity_check_integration_test.go injects one
-- violation per check and expects it to be counted.
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
type_parent_walk AS (
    SELECT t.id AS start, t.parent_type_id AS next, 1 AS depth
    FROM ci_type t, params p
    WHERE t.parent_type_id IS NOT NULL AND (p.org IS NULL OR t.organization_id = p.org)
    UNION ALL
    SELECT w.start, t.parent_type_id, w.depth + 1
    FROM type_parent_walk w JOIN ci_type t ON t.id = w.next
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
scoped_asset AS (
    SELECT a.* FROM asset a, params p WHERE p.org IS NULL OR a.organization_id = p.org
),
scoped_stock AS (
    SELECT s.* FROM stock_level s, params p WHERE p.org IS NULL OR s.organization_id = p.org
),
active_reservation AS (
    SELECT r.* FROM reservation r, params p
    WHERE r.state = 'active' AND (p.org IS NULL OR r.organization_id = p.org)
),
-- The holder the ledger gives each asset: the assignee of its last
-- assignment, none after a return. A holder change books return and
-- assignment at the same occurred_at; the assignment is the later one.
ledger_holder AS (
    SELECT DISTINCT ON (m.asset_id) m.asset_id,
           CASE WHEN m.movement_type = 'assignment' THEN m.assignee_user_id END AS user_id,
           CASE WHEN m.movement_type = 'assignment' THEN m.assignee_team_id END AS team_id
    FROM stock_movement m, params p
    WHERE m.asset_id IS NOT NULL AND m.movement_type IN ('assignment', 'return')
      AND (p.org IS NULL OR m.organization_id = p.org)
    ORDER BY m.asset_id, m.occurred_at DESC, (m.movement_type = 'assignment') DESC, m.created_at DESC, m.id DESC
),
scoped_link AS (
    SELECT l.* FROM document_link l, params p WHERE p.org IS NULL OR l.organization_id = p.org
),
-- The organization of the target of each document link (entity_type,
-- entity_id; no foreign key, 000011); NULL when no row of the target
-- table has the id.
link_target AS (
    SELECT l.id, l.organization_id, CASE l.entity_type
        WHEN 'ci' THEN (SELECT x.organization_id FROM ci x WHERE x.id = l.entity_id)
        WHEN 'asset' THEN (SELECT x.organization_id FROM asset x WHERE x.id = l.entity_id)
        WHEN 'ticket' THEN (SELECT x.organization_id FROM ticket x WHERE x.id = l.entity_id)
        WHEN 'location' THEN (SELECT x.organization_id FROM location x WHERE x.id = l.entity_id)
        WHEN 'disposal_record' THEN (SELECT x.organization_id FROM disposal_record x WHERE x.id = l.entity_id)
        WHEN 'stocktake' THEN (SELECT x.organization_id FROM stocktake x WHERE x.id = l.entity_id)
        WHEN 'contract' THEN (SELECT x.organization_id FROM contract x WHERE x.id = l.entity_id)
        WHEN 'stock_movement' THEN (SELECT x.organization_id FROM stock_movement x WHERE x.id = l.entity_id)
    END AS target_org
    FROM scoped_link l
),
-- Every CI type of a CI with itself and its ancestors (parent_type_id,
-- WP-248); the depth bounds a cycle.
type_chain AS (
    SELECT t.id AS type_id, t.id AS ancestor, 0 AS depth
    FROM ci_type t WHERE t.id IN (SELECT ci_type_id FROM scoped_ci)
    UNION ALL
    SELECT c.type_id, t.parent_type_id, c.depth + 1
    FROM type_chain c JOIN ci_type t ON t.id = c.ancestor
    WHERE t.parent_type_id IS NOT NULL AND c.depth < 100
),
-- The values of the reference-typed and file attributes of the active CIs
-- (WP-254, MET-12; no foreign key): the definitions of the CI's type and
-- its ancestors, of their attribute sets and the global ones, with their
-- target (reticora_reference_target, 000107; file: document). A value is
-- an id or an array of ids.
ref_value AS (
    SELECT c.id AS ci_id, c.organization_id, d.target, v.value
    FROM scoped_ci c
    CROSS JOIN LATERAL (
        SELECT DISTINCT a.key, CASE WHEN a.data_type = 'file' THEN 'document' ELSE reticora_reference_target(a) END AS target
        FROM attribute_definition a
        WHERE (a.organization_id IS NULL OR a.organization_id = c.organization_id)
          AND (a.data_type = 'file' OR reticora_reference_target(a) <> '')
          AND c.attributes ? a.key
          AND (a.scope = 'global'
            OR a.ci_type_id IN (SELECT w.ancestor FROM type_chain w WHERE w.type_id = c.ci_type_id)
            OR a.attribute_set_id IN (SELECT s.attribute_set_id FROM ci_type_attribute_set s
                                       WHERE s.ci_type_id IN (SELECT w.ancestor FROM type_chain w WHERE w.type_id = c.ci_type_id)))
    ) d
    CROSS JOIN LATERAL jsonb_array_elements_text(CASE jsonb_typeof(c.attributes -> d.key)
        WHEN 'array' THEN c.attributes -> d.key
        WHEN 'string' THEN jsonb_build_array(c.attributes -> d.key)
        ELSE '[]'::jsonb END) v(value)
    WHERE c.deleted_at IS NULL AND c.merged_into_id IS NULL
      AND v.value ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
),
-- The organization of the target of each reference value; NULL when no
-- row of the target table has the id.
ref_target AS (
    SELECT r.organization_id, CASE r.target
        WHEN 'ci' THEN (SELECT x.organization_id FROM ci x WHERE x.id = r.value::uuid)
        WHEN 'asset' THEN (SELECT x.organization_id FROM asset x WHERE x.id = r.value::uuid)
        WHEN 'contract' THEN (SELECT x.organization_id FROM contract x WHERE x.id = r.value::uuid)
        WHEN 'user' THEN (SELECT x.organization_id FROM app_user x WHERE x.id = r.value::uuid)
        WHEN 'team' THEN (SELECT x.organization_id FROM team x WHERE x.id = r.value::uuid)
        WHEN 'location' THEN (SELECT x.organization_id FROM location x WHERE x.id = r.value::uuid)
        WHEN 'document' THEN (SELECT x.organization_id FROM document x WHERE x.id = r.value::uuid)
    END AS target_org
    FROM ref_value r
),
-- The single-column foreign keys between two tenant tables that do not
-- carry the organization (rls.ReferencesWithoutOrganization, WP-838): the
-- referencing table has organization_id, the target holds no global rows.
loose_fk AS (
    SELECT c.conrelid::regclass::text AS src, a.attname AS col, c.confrelid::regclass::text AS dst, d.attname AS dst_col
    FROM pg_constraint c
    JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
    JOIN pg_attribute d ON d.attrelid = c.confrelid AND d.attnum = c.confkey[1]
    WHERE c.contype = 'f' AND c.conparentid = 0 AND cardinality(c.conkey) = 1
      AND c.connamespace = 'public'::regnamespace AND a.attname <> 'organization_id'
      AND EXISTS (SELECT 1 FROM pg_attribute x WHERE x.attrelid = c.conrelid
                    AND x.attname = 'organization_id' AND NOT x.attisdropped)
      AND EXISTS (SELECT 1 FROM pg_attribute x WHERE x.attrelid = c.confrelid
                    AND x.attname = 'organization_id' AND NOT x.attisdropped AND x.attnotnull)
),
-- Per such foreign key the rows whose target belongs to another
-- organization (query_to_xml runs the query of each key in this one
-- statement).
loose_ref AS (
    SELECT (xpath('/row/n/text()', query_to_xml(format(
        'SELECT count(*) AS n FROM %s s JOIN %s t ON t.%I = s.%I WHERE t.organization_id <> s.organization_id%s',
        f.src, f.dst, f.dst_col, f.col,
        CASE WHEN p.org IS NULL THEN '' ELSE format(' AND s.organization_id = %L', p.org) END), false, true, '')))[1]::text::bigint AS n
    FROM loose_fk f, params p
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
SELECT 'duplicate: normalized serial number of assets not disposed', count(*) FROM (
    SELECT organization_id, lower(btrim(serial_number)) FROM scoped_asset
    WHERE btrim(COALESCE(serial_number, '')) <> ''
      AND status <> 'disposed' AND lifecycle_state IS DISTINCT FROM 'disposed'
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
SELECT 'cross-tenant: asset type', count(*)
FROM scoped_asset a JOIN ci_type t ON t.id = a.asset_type_id WHERE t.organization_id <> a.organization_id
UNION ALL
SELECT 'cross-tenant: document link document', count(*)
FROM scoped_link l JOIN document d ON d.id = l.document_id WHERE d.organization_id <> l.organization_id
UNION ALL
SELECT 'cross-tenant: document link target', count(*) FROM link_target WHERE target_org <> organization_id
UNION ALL
SELECT 'orphan: document link without target', count(*) FROM link_target WHERE target_org IS NULL
UNION ALL
SELECT 'cross-tenant: CI reference attribute', count(*) FROM ref_target WHERE target_org <> organization_id
UNION ALL
SELECT 'orphan: CI reference attribute without target', count(*) FROM ref_target WHERE target_org IS NULL
UNION ALL
SELECT 'cross-tenant: reference without the organization', COALESCE(sum(n), 0)::bigint FROM loose_ref
UNION ALL
SELECT 'cross-tenant: stock movement context', count(*)
FROM stock_movement m, params p
WHERE (p.org IS NULL OR m.organization_id = p.org)
  AND (EXISTS (SELECT 1 FROM ticket x WHERE x.id = m.ticket_id AND x.organization_id <> m.organization_id)
    OR EXISTS (SELECT 1 FROM internal_order x WHERE x.id = m.order_id AND x.organization_id <> m.organization_id)
    OR EXISTS (SELECT 1 FROM document x WHERE x.id = m.document_id AND x.organization_id <> m.organization_id)
    OR EXISTS (SELECT 1 FROM workflow_run x WHERE x.id = m.workflow_run_id AND x.organization_id <> m.organization_id))
UNION ALL
SELECT 'cycle: CI type lineage', count(*) FROM type_walk WHERE next = start
UNION ALL
SELECT 'cycle: CI type inheritance', count(*) FROM type_parent_walk WHERE next = start
UNION ALL
SELECT 'cycle: location tree', count(*) FROM location_walk WHERE next = start
UNION ALL
SELECT 'cycle: composition', count(*) FROM composition_walk WHERE next = start
UNION ALL
SELECT 'stock: negative on_hand', count(*) FROM scoped_stock WHERE on_hand < 0
UNION ALL
SELECT 'stock: reserved below zero or above on_hand', count(*) FROM scoped_stock WHERE reserved < 0 OR reserved > on_hand
UNION ALL
SELECT 'stock: reserved other than the active quantity reservations', count(*)
FROM scoped_stock s
FULL JOIN (
    SELECT organization_id, consumable_id, location_id, sum(quantity) AS quantity FROM active_reservation
    WHERE item_kind = 'consumable' GROUP BY 1, 2, 3) r
  ON r.consumable_id = s.consumable_id AND r.location_id = s.location_id
WHERE COALESCE(s.reserved, 0) <> COALESCE(r.quantity, 0)
UNION ALL
SELECT 'reservation: active reservation of an asset held by another holder', count(*)
FROM active_reservation r JOIN asset a ON a.id = r.asset_id
WHERE r.item_kind = 'asset'
  AND (a.assigned_team_id IS NOT NULL OR (a.assigned_user_id IS NOT NULL AND a.assigned_user_id IS DISTINCT FROM r.assignee_id))
UNION ALL
SELECT 'assignment: holder cache other than the assignment and return movements', count(*)
FROM scoped_asset a LEFT JOIN ledger_holder h ON h.asset_id = a.id
WHERE a.assigned_user_id IS DISTINCT FROM h.user_id OR a.assigned_team_id IS DISTINCT FROM h.team_id;
