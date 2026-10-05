-- WP-054 (LOC-10, DB-05): CI and asset locations reference the canonical
-- location tree; the side tree location_node is retired.
--
-- 1. Storage nodes created since 000062 are imported into location.
-- 2. Every location_node is mapped to a location: site/building/room/rack
--    nodes to the object they stand for, storage nodes to themselves (same
--    id), any other node (floor, desk, vehicle, ...) to its nearest mapped
--    ancestor. The nodes are kept in location_node_retired (E-26: no data is
--    dropped; the down migration restores them).
-- 3. ci.location_id, asset.location_id, asset_movement.from/to_location_id
--    and quantity_item.location_id are re-pointed through the mapping and
--    reference location. A CI without location but with room or site gets
--    that room or site as location. Every changed reference is recorded in
--    location_ref_migration for the down migration.
-- 4. ci.site_id and ci.room_id are derived by trigger from ci.location_id
--    (DB-05: the location of a logical CI is ci.location_id, site and room
--    are denormalized); they follow when the location moves. A location of
--    another client than the CI's is rejected; an org-wide CI (no client)
--    may have any location of the organization. A CI whose client
--    contradicts its location keeps its client and loses the location
--    (recorded with reason client_mismatch).
-- 5. Search, document and AI rows of entity type location are re-pointed;
--    the search scope is derived from location.
-- 6. location_node and location_import_nodes are dropped.

-- ─── 1. Late storage nodes ──────────────────────────────────────────────────
SELECT location_import_nodes();

-- ─── 2. Mapping and archive ─────────────────────────────────────────────────
CREATE TABLE location_node_retired (
    id              UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id       UUID,
    site_id         UUID,
    location_id     UUID,
    node            JSONB NOT NULL,
    retired_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE location_node_retired IS
    'Rows of the retired location_node tree (WP-054) with the location each was mapped to; read by the down migration.';

ALTER TABLE location_node_retired ENABLE ROW LEVEL SECURITY;
ALTER TABLE location_node_retired FORCE ROW LEVEL SECURITY;
CREATE POLICY location_node_retired_isolation ON location_node_retired
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

INSERT INTO location_node_retired (id, organization_id, client_id, site_id, location_id, node)
WITH RECURSIVE mapped AS (
    SELECT n.id, n.organization_id,
           (SELECT l.id FROM location l
             WHERE l.organization_id = n.organization_id
               AND l.id = CASE n.node_type
                              WHEN 'site' THEN n.site_id
                              WHEN 'building' THEN n.building_id
                              WHEN 'room' THEN n.room_id
                              WHEN 'rack' THEN n.rack_id
                              ELSE n.id
                          END) AS location_id
      FROM location_node n
     WHERE n.parent_id IS NULL
    UNION ALL
    SELECT n.id, n.organization_id,
           COALESCE((SELECT l.id FROM location l
                      WHERE l.organization_id = n.organization_id
                        AND l.id = CASE n.node_type
                                       WHEN 'site' THEN n.site_id
                                       WHEN 'building' THEN n.building_id
                                       WHEN 'room' THEN n.room_id
                                       WHEN 'rack' THEN n.rack_id
                                       ELSE n.id
                                   END),
                    m.location_id)
      FROM location_node n
      JOIN mapped m ON m.id = n.parent_id
)
SELECT m.id, m.organization_id, l.client_id, l.site_id, m.location_id, to_jsonb(n)
  FROM mapped m
  JOIN location_node n ON n.id = m.id
  LEFT JOIN location l ON l.id = m.location_id;

CREATE TABLE location_ref_migration (
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    table_name      TEXT NOT NULL,
    row_id          UUID NOT NULL,
    column_name     TEXT NOT NULL,
    old_value       UUID,
    new_value       UUID,
    reason          TEXT NOT NULL,
    PRIMARY KEY (table_name, row_id, column_name)
);

COMMENT ON TABLE location_ref_migration IS
    'Location references changed by WP-054 (migration 000074) with their previous value; read by the down migration.';

ALTER TABLE location_ref_migration ENABLE ROW LEVEL SECURITY;
ALTER TABLE location_ref_migration FORCE ROW LEVEL SECURITY;
CREATE POLICY location_ref_migration_isolation ON location_ref_migration
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- ─── 3. Re-point the references ─────────────────────────────────────────────
ALTER TABLE ci DROP CONSTRAINT ci_location_id_fkey;
ALTER TABLE asset DROP CONSTRAINT asset_location_id_fkey;
ALTER TABLE asset_movement DROP CONSTRAINT asset_movement_from_location_id_fkey;
ALTER TABLE asset_movement DROP CONSTRAINT asset_movement_to_location_id_fkey;
ALTER TABLE quantity_item DROP CONSTRAINT quantity_item_location_id_fkey;

-- CI: the previous location, site and room of every located CI, so the down
-- migration restores them exactly.
INSERT INTO location_ref_migration (organization_id, table_name, row_id, column_name, old_value, reason)
SELECT c.organization_id, 'ci', c.id, col.name, col.value, 'ci_location'
  FROM ci c
 CROSS JOIN LATERAL (VALUES ('location_id', c.location_id), ('site_id', c.site_id), ('room_id', c.room_id)) AS col(name, value)
 WHERE c.location_id IS NOT NULL OR c.site_id IS NOT NULL OR c.room_id IS NOT NULL;

UPDATE ci c
   SET location_id = COALESCE(r.location_id, CASE WHEN c.location_id IS NULL THEN COALESCE(c.room_id, c.site_id) END)
  FROM (SELECT c2.id, r2.location_id
          FROM ci c2 LEFT JOIN location_node_retired r2 ON r2.id = c2.location_id) r
 WHERE r.id = c.id
   AND (c.location_id IS NOT NULL OR c.site_id IS NOT NULL OR c.room_id IS NOT NULL);

-- A location id that is neither a location nor a mapped node is dropped.
UPDATE ci SET location_id = NULL
 WHERE location_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM location l WHERE l.id = ci.location_id);

-- A CI whose client contradicts its location keeps its client and loses the
-- location.
UPDATE location_ref_migration m SET reason = 'client_mismatch'
  FROM ci c JOIN location l ON l.id = c.location_id
 WHERE m.table_name = 'ci' AND m.row_id = c.id
   AND c.client_id IS NOT NULL AND c.client_id <> l.client_id;
UPDATE ci SET location_id = NULL
  FROM location l
 WHERE l.id = ci.location_id AND ci.client_id IS NOT NULL AND ci.client_id <> l.client_id;

UPDATE location_ref_migration m SET new_value = c.location_id
  FROM ci c
 WHERE m.table_name = 'ci' AND m.row_id = c.id AND m.column_name = 'location_id';

-- Asset, movement and stock references: record and re-point the node ids.
INSERT INTO location_ref_migration (organization_id, table_name, row_id, column_name, old_value, new_value, reason)
SELECT a.organization_id, 'asset', a.id, 'location_id', a.location_id, r.location_id, 'node_mapping'
  FROM asset a LEFT JOIN location_node_retired r ON r.id = a.location_id
 WHERE a.location_id IS NOT NULL
UNION ALL
SELECT m.organization_id, 'asset_movement', m.id, 'from_location_id', m.from_location_id, r.location_id, 'node_mapping'
  FROM asset_movement m LEFT JOIN location_node_retired r ON r.id = m.from_location_id
 WHERE m.from_location_id IS NOT NULL
UNION ALL
SELECT m.organization_id, 'asset_movement', m.id, 'to_location_id', m.to_location_id, r.location_id, 'node_mapping'
  FROM asset_movement m LEFT JOIN location_node_retired r ON r.id = m.to_location_id
 WHERE m.to_location_id IS NOT NULL
UNION ALL
SELECT q.organization_id, 'quantity_item', q.id, 'location_id', q.location_id, r.location_id, 'node_mapping'
  FROM quantity_item q LEFT JOIN location_node_retired r ON r.id = q.location_id
 WHERE q.location_id IS NOT NULL;

UPDATE asset a SET location_id = m.new_value
  FROM location_ref_migration m
 WHERE m.table_name = 'asset' AND m.row_id = a.id AND m.column_name = 'location_id';
-- The movement ledger is append-only; only this migration rewrites its
-- location references.
ALTER TABLE asset_movement DISABLE TRIGGER trg_asset_movement_no_update;
UPDATE asset_movement a SET from_location_id = m.new_value
  FROM location_ref_migration m
 WHERE m.table_name = 'asset_movement' AND m.row_id = a.id AND m.column_name = 'from_location_id';
UPDATE asset_movement a SET to_location_id = m.new_value
  FROM location_ref_migration m
 WHERE m.table_name = 'asset_movement' AND m.row_id = a.id AND m.column_name = 'to_location_id';
ALTER TABLE asset_movement ENABLE TRIGGER trg_asset_movement_no_update;
UPDATE quantity_item q SET location_id = m.new_value
  FROM location_ref_migration m
 WHERE m.table_name = 'quantity_item' AND m.row_id = q.id AND m.column_name = 'location_id';

-- Rows that were already location ids need no change record.
DELETE FROM location_ref_migration
 WHERE reason = 'node_mapping' AND old_value IS NOT DISTINCT FROM new_value;

ALTER TABLE ci ADD CONSTRAINT ci_location_id_fkey
    FOREIGN KEY (location_id) REFERENCES location(id);
ALTER TABLE asset ADD CONSTRAINT asset_location_id_fkey
    FOREIGN KEY (location_id) REFERENCES location(id);
ALTER TABLE asset_movement ADD CONSTRAINT asset_movement_from_location_id_fkey
    FOREIGN KEY (from_location_id) REFERENCES location(id) ON DELETE SET NULL;
ALTER TABLE asset_movement ADD CONSTRAINT asset_movement_to_location_id_fkey
    FOREIGN KEY (to_location_id) REFERENCES location(id) ON DELETE SET NULL;
ALTER TABLE quantity_item ADD CONSTRAINT quantity_item_location_id_fkey
    FOREIGN KEY (location_id) REFERENCES location(id) ON DELETE SET NULL;

-- ─── 4. Derive site and room of a CI ────────────────────────────────────────
-- ci_derive_location sets site_id and room_id of a CI from its location;
-- written values are overwritten. The room is the location itself or its
-- nearest room ancestor.
CREATE FUNCTION ci_derive_location() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    loc RECORD;
BEGIN
    IF NEW.location_id IS NULL THEN
        NEW.site_id := NULL;
        NEW.room_id := NULL;
        RETURN NEW;
    END IF;
    SELECT l.client_id, l.site_id, l.path INTO loc FROM location l WHERE l.id = NEW.location_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'location % not found', NEW.location_id
            USING ERRCODE = '23503', CONSTRAINT = 'ci_location_id_fkey';
    END IF;
    IF NEW.client_id IS NOT NULL AND NEW.client_id <> loc.client_id
       AND (TG_OP = 'INSERT' OR NEW.client_id IS DISTINCT FROM OLD.client_id
            OR NEW.location_id IS DISTINCT FROM OLD.location_id) THEN
        RAISE EXCEPTION 'location % belongs to another client', NEW.location_id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_location_client';
    END IF;
    NEW.site_id := loc.site_id;
    NEW.room_id := (SELECT r.id FROM location r WHERE r.kind = 'room' AND r.path @> loc.path LIMIT 1);
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_ci_derive_location BEFORE INSERT OR UPDATE OF location_id, site_id, room_id, client_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_derive_location();

-- location_refresh_ci re-derives the CIs of a moved location or of one whose
-- site or client changed; the descendants of a moved node are updated by
-- location_propagate and fire this trigger themselves.
CREATE FUNCTION location_refresh_ci() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ci SET location_id = location_id WHERE location_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_location_refresh_ci AFTER UPDATE ON location
    FOR EACH ROW WHEN (OLD.path IS DISTINCT FROM NEW.path
                       OR OLD.site_id IS DISTINCT FROM NEW.site_id
                       OR OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION location_refresh_ci();

UPDATE ci SET location_id = location_id
 WHERE location_id IS NOT NULL OR site_id IS NOT NULL OR room_id IS NOT NULL;

-- ─── 5. Entity references of type location ──────────────────────────────────
INSERT INTO location_ref_migration (organization_id, table_name, row_id, column_name, old_value, new_value, reason)
SELECT d.organization_id, 'document_link', d.id, 'entity_id', d.entity_id, r.location_id, 'node_mapping'
  FROM document_link d JOIN location_node_retired r ON r.id = d.entity_id
 WHERE d.entity_type = 'location' AND r.location_id IS NOT NULL AND r.location_id <> d.entity_id
UNION ALL
SELECT a.organization_id, 'ai_chunk', a.id, 'entity_id', a.entity_id, r.location_id, 'node_mapping'
  FROM ai_chunk a JOIN location_node_retired r ON r.id = a.entity_id
 WHERE a.entity_type = 'location' AND r.location_id IS NOT NULL AND r.location_id <> a.entity_id;

UPDATE document_link d SET entity_id = m.new_value
  FROM location_ref_migration m
 WHERE m.table_name = 'document_link' AND m.row_id = d.id;
UPDATE ai_chunk a SET entity_id = m.new_value
  FROM location_ref_migration m
 WHERE m.table_name = 'ai_chunk' AND m.row_id = a.id;

-- The search index is rebuilt by the reindex; stale location rows go.
DELETE FROM search_document s
 WHERE s.entity_type = 'location' AND NOT EXISTS (SELECT 1 FROM location l WHERE l.id = s.entity_id);

CREATE OR REPLACE FUNCTION derive_search_document_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.entity_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id INTO NEW.client_id, NEW.site_id FROM ticket t WHERE t.id = NEW.entity_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id FROM location l WHERE l.id = NEW.entity_id;
        WHEN 'asset' THEN
            SELECT a.client_id INTO NEW.client_id FROM asset a WHERE a.id = NEW.entity_id;
        WHEN 'contact' THEN
            SELECT c.client_id INTO NEW.client_id FROM contact c WHERE c.id = NEW.entity_id;
        WHEN 'reservation' THEN
            SELECT a.client_id INTO NEW.client_id
              FROM reservation r JOIN asset a ON a.id = r.asset_id WHERE r.id = NEW.entity_id;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

DROP TRIGGER location_node_search_scope_propagation ON location_node;
CREATE TRIGGER location_search_scope_propagation AFTER UPDATE ON location
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('location');

UPDATE search_document SET entity_id = entity_id WHERE entity_type = 'location';

-- ─── 6. Retire location_node ────────────────────────────────────────────────
DROP FUNCTION location_import_nodes(UUID);
DROP TABLE location_node;
