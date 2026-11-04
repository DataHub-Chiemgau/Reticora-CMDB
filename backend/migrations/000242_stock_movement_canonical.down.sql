-- Reverts 000242_stock_movement_canonical.
--
-- 1. Links to movements are removed; doc_link_entity_type_check and
--    derive_document_link_scope return to their state after 000238.
-- 2. The ledger is renamed out of the way and the former stock_movement
--    (state of 000045, with reticora_truncate_guard) is created again. Its
--    consumable rows come back with their ids (receipt → in, every other
--    type → out, notes → reference), and so do the rows the up migration
--    quarantined.
-- 3. Asset and quantity item rows whose id is missing in asset_movement
--    (booked after the up migration) are written back there with the former
--    type names (transfer → warehouse_transfer, release →
--    reservation_release, repair → repair_transfer). Rows taken from
--    assignment (notes 'assignment:<id>') are dropped: assignment still
--    holds them.
-- 4. The quarantine entries of the up migration are removed, the ledger, its
--    functions and UNIQUE (id, organization_id) of quantity_item are
--    dropped.

-- ─── 1. Document links ─────────────────────────────────────────────────────────

DELETE FROM document_link WHERE entity_type = 'stock_movement';

CREATE OR REPLACE FUNCTION derive_document_link_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    NEW.team_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
              FROM ci c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM asset a WHERE a.id = NEW.entity_id AND a.organization_id = NEW.organization_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id, t.team_id INTO NEW.client_id, NEW.site_id, NEW.team_id
              FROM ticket t WHERE t.id = NEW.entity_id AND t.organization_id = NEW.organization_id;
        WHEN 'assignment' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM assignment s JOIN asset a ON a.id = s.asset_id
             WHERE s.id = NEW.entity_id AND s.organization_id = NEW.organization_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id
              FROM location l WHERE l.id = NEW.entity_id AND l.organization_id = NEW.organization_id;
        WHEN 'disposal_record' THEN
            SELECT d.client_id, d.site_id INTO NEW.client_id, NEW.site_id
              FROM disposal_record d WHERE d.id = NEW.entity_id AND d.organization_id = NEW.organization_id;
        WHEN 'contract' THEN
            SELECT c.client_id INTO NEW.client_id
              FROM contract c WHERE c.id = NEW.entity_id AND c.organization_id = NEW.organization_id;
        ELSE
            NULL; -- stocktake: org-wide
    END CASE;
    RETURN NEW;
END
$$;

ALTER TABLE document_link DROP CONSTRAINT doc_link_entity_type_check;
ALTER TABLE document_link ADD CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
    'ci', 'asset', 'assignment', 'ticket', 'location', 'disposal_record', 'stocktake', 'contract'
));

-- ─── 2. Former stock_movement ──────────────────────────────────────────────────

ALTER TABLE stock_movement RENAME TO stock_movement_ledger;
ALTER INDEX stock_movement_pkey RENAME TO stock_movement_ledger_pkey;
ALTER INDEX stock_movement_id_organization_key RENAME TO stock_movement_ledger_id_organization_key;
ALTER INDEX idx_stock_movement_org RENAME TO idx_stock_movement_ledger_org;
ALTER INDEX idx_stock_movement_asset RENAME TO idx_stock_movement_ledger_asset;
ALTER INDEX idx_stock_movement_consumable RENAME TO idx_stock_movement_ledger_consumable;
ALTER INDEX idx_stock_movement_quantity_item RENAME TO idx_stock_movement_ledger_quantity_item;
ALTER INDEX idx_stock_movement_from_location RENAME TO idx_stock_movement_ledger_from_location;
ALTER INDEX idx_stock_movement_to_location RENAME TO idx_stock_movement_ledger_to_location;
ALTER INDEX idx_stock_movement_type RENAME TO idx_stock_movement_ledger_type;

-- The constraints are named like 000045 created them: the renamed ledger
-- still holds constraints of these names, generated names would differ.
CREATE TABLE stock_movement (
    id              UUID CONSTRAINT stock_movement_pkey PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL CONSTRAINT stock_movement_organization_id_fkey REFERENCES organization(id) ON DELETE CASCADE,
    consumable_id   UUID NOT NULL CONSTRAINT stock_movement_consumable_id_fkey REFERENCES consumable(id) ON DELETE CASCADE,
    direction       TEXT NOT NULL CONSTRAINT stock_movement_direction_check CHECK (direction IN ('in', 'out')),
    quantity        NUMERIC(12,2) NOT NULL CONSTRAINT stock_movement_quantity_check CHECK (quantity > 0),
    reason          TEXT,
    reference       TEXT,
    actor_id        UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE stock_movement ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_movement FORCE ROW LEVEL SECURITY;
CREATE POLICY stock_movement_isolation ON stock_movement
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE INDEX idx_stock_movement_consumable ON stock_movement(consumable_id);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON stock_movement
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

INSERT INTO stock_movement (id, organization_id, consumable_id, direction, quantity, reason, reference, actor_id, created_at)
SELECT m.id, m.organization_id, m.consumable_id,
       CASE m.movement_type WHEN 'receipt' THEN 'in' ELSE 'out' END,
       m.quantity, m.reason, m.notes, m.actor_id, m.created_at
  FROM stock_movement_ledger m
 WHERE m.item_kind = 'consumable';

INSERT INTO stock_movement (id, organization_id, consumable_id, direction, quantity, reason, reference, actor_id, created_at)
SELECT r.id, r.organization_id, r.consumable_id, r.direction, r.quantity, r.reason, r.reference, r.actor_id, r.created_at
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::stock_movement, q.row_data) r
 WHERE q.migration = '000242' AND q.source_table = 'stock_movement'
   AND EXISTS (SELECT 1 FROM consumable c WHERE c.id = r.consumable_id)
   AND NOT EXISTS (SELECT 1 FROM stock_movement s WHERE s.id = r.id);

-- ─── 3. Rows booked after the up migration ─────────────────────────────────────

INSERT INTO asset_movement (
    id, organization_id, item_kind, asset_id, quantity_item_id, movement_type,
    from_location_id, to_location_id, quantity, actor_id, reason, ticket_id,
    order_id, workflow_run_id, document_id, notes, created_at)
SELECT m.id, m.organization_id, m.item_kind, m.asset_id, m.quantity_item_id,
       CASE m.movement_type
           WHEN 'transfer' THEN 'warehouse_transfer'
           WHEN 'release' THEN 'reservation_release'
           WHEN 'repair' THEN 'repair_transfer'
           ELSE m.movement_type
       END,
       m.from_location_id, m.to_location_id, m.quantity, m.actor_id, m.reason, m.ticket_id,
       m.order_id, m.workflow_run_id, m.document_id, m.notes, m.created_at
  FROM stock_movement_ledger m
 WHERE m.item_kind IN ('asset', 'quantity_item')
   AND (m.notes IS NULL OR m.notes !~ '^assignment:[0-9a-f-]{36}$')
   AND NOT EXISTS (SELECT 1 FROM asset_movement a WHERE a.id = m.id);

-- ─── 4. Cleanup ────────────────────────────────────────────────────────────────

DELETE FROM migration_quarantine WHERE migration = '000242';

DROP TABLE stock_movement_ledger;
DROP FUNCTION derive_stock_movement_scope();
DROP FUNCTION reject_stock_movement_mutation();
ALTER TABLE quantity_item DROP CONSTRAINT quantity_item_id_organization_key;
