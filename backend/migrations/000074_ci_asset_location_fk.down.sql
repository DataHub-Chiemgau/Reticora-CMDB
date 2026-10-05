-- Reverts 000074: restores location_node from location_node_retired, points
-- the references back to it and restores the recorded values.
--
-- References written after the up migration that are not location_node ids
-- (new CIs located at a site, room or storage location) cannot be expressed
-- in the old model and are cleared; site_id and room_id of those CIs stay.

-- ─── 6. location_node and its import function ───────────────────────────────
CREATE TABLE location_node (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID REFERENCES client(id) ON DELETE SET NULL,
    parent_id UUID REFERENCES location_node(id) ON DELETE CASCADE,
    node_type TEXT NOT NULL,
    name TEXT NOT NULL,
    site_id UUID REFERENCES site(id) ON DELETE SET NULL,
    building_id UUID REFERENCES building(id) ON DELETE SET NULL,
    room_id UUID REFERENCES room(id) ON DELETE SET NULL,
    rack_id UUID REFERENCES rack(id) ON DELETE SET NULL,
    barcode TEXT,
    attributes JSONB NOT NULL DEFAULT '{}',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE location_node ENABLE ROW LEVEL SECURITY;
ALTER TABLE location_node FORCE ROW LEVEL SECURITY;
CREATE POLICY location_node_isolation ON location_node
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

CREATE INDEX idx_location_node_org ON location_node(organization_id);
CREATE INDEX idx_location_node_parent ON location_node(parent_id);
CREATE INDEX idx_location_node_type ON location_node(organization_id, node_type);
CREATE INDEX idx_location_node_client ON location_node(client_id) WHERE client_id IS NOT NULL;

CREATE TRIGGER trg_location_node_updated_at BEFORE UPDATE ON location_node
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Parents are linked after all rows exist. References to site, building,
-- room or rack rows deleted in the meantime are cleared like ON DELETE SET
-- NULL would have.
INSERT INTO location_node (id, organization_id, client_id, node_type, name, site_id, building_id,
                           room_id, rack_id, barcode, attributes, sort_order, created_at, updated_at)
SELECT n.id, n.organization_id,
       (SELECT c.id FROM client c WHERE c.id = n.client_id),
       n.node_type, n.name,
       (SELECT s.id FROM site s WHERE s.id = n.site_id),
       (SELECT b.id FROM building b WHERE b.id = n.building_id),
       (SELECT r.id FROM room r WHERE r.id = n.room_id),
       (SELECT k.id FROM rack k WHERE k.id = n.rack_id),
       n.barcode, COALESCE(n.attributes, '{}'), COALESCE(n.sort_order, 0), n.created_at, n.updated_at
  FROM location_node_retired lr
 CROSS JOIN LATERAL jsonb_populate_record(NULL::location_node, lr.node) n;

UPDATE location_node n SET parent_id = (lr.node ->> 'parent_id')::uuid
  FROM location_node_retired lr
 WHERE lr.id = n.id AND lr.node ->> 'parent_id' IS NOT NULL
   AND EXISTS (SELECT 1 FROM location_node p WHERE p.id = (lr.node ->> 'parent_id')::uuid);

CREATE FUNCTION location_import_nodes(org UUID DEFAULT NULL) RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    node RECORD;
    skipped INTEGER := 0;
BEGIN
    FOR node IN
        WITH RECURSIVE tree AS (
            SELECT n.*, 0 AS depth FROM location_node n
             WHERE n.parent_id IS NULL AND (org IS NULL OR n.organization_id = org)
            UNION ALL
            SELECT n.*, t.depth + 1 FROM location_node n JOIN tree t ON n.parent_id = t.id
        )
        SELECT t.id, t.organization_id, t.node_type, t.name,
               CASE p.node_type
                   WHEN 'site' THEN p.site_id
                   WHEN 'building' THEN p.building_id
                   WHEN 'room' THEN p.room_id
                   WHEN 'rack' THEN p.rack_id
                   ELSE p.id
               END AS parent_location
          FROM tree t
          LEFT JOIN location_node p ON p.id = t.parent_id
         WHERE t.node_type IN ('warehouse', 'zone', 'shelf', 'bin')
           AND NOT EXISTS (SELECT 1 FROM location l WHERE l.id = t.id)
         ORDER BY t.depth
    LOOP
        BEGIN
            INSERT INTO location (id, organization_id, kind, parent_id, name)
            VALUES (node.id, node.organization_id, node.node_type, node.parent_location, node.name);
        EXCEPTION WHEN integrity_constraint_violation THEN
            skipped := skipped + 1;
            RAISE NOTICE 'location_node % (%) not imported: %', node.id, node.node_type, SQLERRM;
        END;
    END LOOP;
    RETURN skipped;
END
$$;

-- ─── 5. Search scope and entity references ──────────────────────────────────
DROP TRIGGER location_search_scope_propagation ON location;
CREATE TRIGGER location_node_search_scope_propagation AFTER UPDATE ON location_node
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('location');

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
            SELECT n.client_id, n.site_id INTO NEW.client_id, NEW.site_id FROM location_node n WHERE n.id = NEW.entity_id;
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

UPDATE document_link d SET entity_id = m.old_value
  FROM location_ref_migration m
 WHERE m.table_name = 'document_link' AND m.row_id = d.id;
UPDATE ai_chunk a SET entity_id = m.old_value
  FROM location_ref_migration m
 WHERE m.table_name = 'ai_chunk' AND m.row_id = a.id;

DELETE FROM search_document s
 WHERE s.entity_type = 'location' AND NOT EXISTS (SELECT 1 FROM location_node n WHERE n.id = s.entity_id);
UPDATE search_document SET entity_id = entity_id WHERE entity_type = 'location';

-- ─── 4. Derivation of site and room ─────────────────────────────────────────
DROP TRIGGER trg_location_refresh_ci ON location;
DROP FUNCTION location_refresh_ci();
DROP TRIGGER trg_ci_derive_location ON ci;
DROP FUNCTION ci_derive_location();

-- ─── 3. References back to location_node ────────────────────────────────────
ALTER TABLE ci DROP CONSTRAINT ci_location_id_fkey;
ALTER TABLE asset DROP CONSTRAINT asset_location_id_fkey;
ALTER TABLE asset_movement DROP CONSTRAINT asset_movement_from_location_id_fkey;
ALTER TABLE asset_movement DROP CONSTRAINT asset_movement_to_location_id_fkey;
ALTER TABLE quantity_item DROP CONSTRAINT quantity_item_location_id_fkey;

UPDATE ci c SET location_id = m.old_value
  FROM location_ref_migration m
 WHERE m.table_name = 'ci' AND m.row_id = c.id AND m.column_name = 'location_id';
UPDATE ci c SET site_id = (SELECT s.id FROM site s WHERE s.id = m.old_value)
  FROM location_ref_migration m
 WHERE m.table_name = 'ci' AND m.row_id = c.id AND m.column_name = 'site_id';
UPDATE ci c SET room_id = (SELECT r.id FROM room r WHERE r.id = m.old_value)
  FROM location_ref_migration m
 WHERE m.table_name = 'ci' AND m.row_id = c.id AND m.column_name = 'room_id';
UPDATE asset a SET location_id = m.old_value
  FROM location_ref_migration m
 WHERE m.table_name = 'asset' AND m.row_id = a.id AND m.column_name = 'location_id';
ALTER TABLE asset_movement DISABLE TRIGGER trg_asset_movement_no_update;
UPDATE asset_movement a SET from_location_id = m.old_value
  FROM location_ref_migration m
 WHERE m.table_name = 'asset_movement' AND m.row_id = a.id AND m.column_name = 'from_location_id';
UPDATE asset_movement a SET to_location_id = m.old_value
  FROM location_ref_migration m
 WHERE m.table_name = 'asset_movement' AND m.row_id = a.id AND m.column_name = 'to_location_id';
ALTER TABLE asset_movement ENABLE TRIGGER trg_asset_movement_no_update;
UPDATE quantity_item q SET location_id = m.old_value
  FROM location_ref_migration m
 WHERE m.table_name = 'quantity_item' AND m.row_id = q.id AND m.column_name = 'location_id';

UPDATE ci SET location_id = NULL
 WHERE location_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM location_node n WHERE n.id = ci.location_id);
UPDATE asset SET location_id = NULL
 WHERE location_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM location_node n WHERE n.id = asset.location_id);
ALTER TABLE asset_movement DISABLE TRIGGER trg_asset_movement_no_update;
UPDATE asset_movement SET from_location_id = NULL
 WHERE from_location_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM location_node n WHERE n.id = asset_movement.from_location_id);
UPDATE asset_movement SET to_location_id = NULL
 WHERE to_location_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM location_node n WHERE n.id = asset_movement.to_location_id);
ALTER TABLE asset_movement ENABLE TRIGGER trg_asset_movement_no_update;
UPDATE quantity_item SET location_id = NULL
 WHERE location_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM location_node n WHERE n.id = quantity_item.location_id);

ALTER TABLE ci ADD CONSTRAINT ci_location_id_fkey
    FOREIGN KEY (location_id) REFERENCES location_node(id) ON DELETE SET NULL;
ALTER TABLE asset ADD CONSTRAINT asset_location_id_fkey
    FOREIGN KEY (location_id) REFERENCES location_node(id) ON DELETE SET NULL;
ALTER TABLE asset_movement ADD CONSTRAINT asset_movement_from_location_id_fkey
    FOREIGN KEY (from_location_id) REFERENCES location_node(id) ON DELETE SET NULL;
ALTER TABLE asset_movement ADD CONSTRAINT asset_movement_to_location_id_fkey
    FOREIGN KEY (to_location_id) REFERENCES location_node(id) ON DELETE SET NULL;
ALTER TABLE quantity_item ADD CONSTRAINT quantity_item_location_id_fkey
    FOREIGN KEY (location_id) REFERENCES location_node(id) ON DELETE SET NULL;

-- ─── 2. Archive tables ──────────────────────────────────────────────────────
DROP TABLE location_ref_migration;
DROP TABLE location_node_retired;
