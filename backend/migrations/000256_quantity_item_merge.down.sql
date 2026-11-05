-- Reverts 000256_quantity_item_merge (WP-757).
--
-- 1. stock_movement gets quantity_item_id back (foreign key and index of
--    000242). The rows of a consumable whose id names a quantity_item (the
--    merged articles, also those booked after the up migration) return to
--    item_kind quantity_item, with the append-only trigger disabled only
--    for this statement. stock_movement_item_kind_check,
--    stock_movement_object_check and derive_stock_movement_scope return to
--    their state of 000242.
-- 2. The stock_level rows and the consumable rows of the merged articles
--    are deleted (their register rows follow by ON DELETE CASCADE, order
--    items lose the reference by SET NULL); quantity_item still holds the
--    articles, unchanged by the up migration. The quarantine entries of
--    000256 are removed.

-- ─── 1. Ledger ────────────────────────────────────────────────────────────────

ALTER TABLE stock_movement ADD COLUMN quantity_item_id UUID,
    ADD CONSTRAINT stock_movement_quantity_item_id_fkey FOREIGN KEY (quantity_item_id, organization_id)
        REFERENCES quantity_item (id, organization_id) ON DELETE RESTRICT;
CREATE INDEX idx_stock_movement_quantity_item ON stock_movement (quantity_item_id, occurred_at DESC) WHERE quantity_item_id IS NOT NULL;

ALTER TABLE stock_movement DROP CONSTRAINT stock_movement_object_check,
    DROP CONSTRAINT stock_movement_item_kind_check;

ALTER TABLE stock_movement DISABLE TRIGGER stock_movement_append_only;
UPDATE stock_movement m
   SET quantity_item_id = m.consumable_id, consumable_id = NULL, item_kind = 'quantity_item'
 WHERE m.consumable_id IS NOT NULL
   AND EXISTS (SELECT 1 FROM quantity_item q WHERE q.id = m.consumable_id AND q.organization_id = m.organization_id);
ALTER TABLE stock_movement ENABLE TRIGGER stock_movement_append_only;

ALTER TABLE stock_movement
    ADD CONSTRAINT stock_movement_item_kind_check CHECK (item_kind IN ('asset', 'consumable', 'quantity_item')),
    ADD CONSTRAINT stock_movement_object_check CHECK (num_nonnulls(asset_id, consumable_id, quantity_item_id) = 1
        AND CASE item_kind
                WHEN 'asset' THEN asset_id IS NOT NULL
                WHEN 'consumable' THEN consumable_id IS NOT NULL
                ELSE quantity_item_id IS NOT NULL
            END);

-- derive_stock_movement_scope reads the object and the locations under the
-- writer's row level security (no app_scope_widen): what the writer cannot
-- see derives NULL.
CREATE OR REPLACE FUNCTION derive_stock_movement_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    object_site UUID;
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.item_kind
        WHEN 'asset' THEN
            SELECT a.client_id, a.site_id INTO NEW.client_id, object_site
              FROM asset a WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
        WHEN 'consumable' THEN
            SELECT c.client_id INTO NEW.client_id
              FROM consumable c WHERE c.id = NEW.consumable_id AND c.organization_id = NEW.organization_id;
        WHEN 'quantity_item' THEN
            SELECT q.client_id, q.site_id INTO NEW.client_id, object_site
              FROM quantity_item q WHERE q.id = NEW.quantity_item_id AND q.organization_id = NEW.organization_id;
        ELSE
            NULL; -- refused by stock_movement_item_kind_check
    END CASE;
    IF NEW.to_location_id IS NOT NULL THEN
        SELECT l.site_id INTO NEW.site_id
          FROM location l WHERE l.id = NEW.to_location_id AND l.organization_id = NEW.organization_id;
    ELSIF NEW.from_location_id IS NOT NULL THEN
        SELECT l.site_id INTO NEW.site_id
          FROM location l WHERE l.id = NEW.from_location_id AND l.organization_id = NEW.organization_id;
    ELSE
        NEW.site_id := object_site;
    END IF;
    RETURN NEW;
END
$$;

-- ─── 2. Articles and stock ────────────────────────────────────────────────────

DELETE FROM stock_level s
 WHERE EXISTS (SELECT 1 FROM quantity_item q WHERE q.id = s.consumable_id AND q.organization_id = s.organization_id);

DELETE FROM consumable c
 WHERE EXISTS (SELECT 1 FROM quantity_item q WHERE q.id = c.id AND q.organization_id = c.organization_id);

DELETE FROM migration_quarantine WHERE migration = '000256';
