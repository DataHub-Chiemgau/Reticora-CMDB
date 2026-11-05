-- Migration 000256: quantity_item merged into consumable and stock_level
-- (STK-03, API-06, WP-757, E-40, E-26).
--
-- 1. Every quantity_item becomes a consumable with the same id, so ledger
--    rows, reservations and order items stay valid without rekeying. The
--    columns id, organization_id, client_id, sku, name, category, unit,
--    min_level, notes, created_at and updated_at are taken over. A SKU
--    that an existing consumable of the organization holds already becomes
--    NULL (reason sku_conflict); non-empty attributes have no target column
--    (reason attributes_dropped). Both go with the full row to
--    migration_quarantine (E-26).
-- 2. Stock: a positive stock_level at a location of kind bin whose client
--    the item admits (item without client, or the client of the bin)
--    becomes a stock_level row with that on_hand (its derivation trigger
--    sets client and site of the bin). Stock without location or at a
--    location of another kind goes to migration_quarantine with reason
--    stock_without_bin, stock in a bin of another client with
--    bin_client_mismatch; a negative level counts as 0 (reason
--    negative_stock). No ledger booking arises; the operator books the
--    quarantined stock as a correction into a bin. quantity_item itself is
--    left unchanged and no longer maintained (dropped by WP-758).
-- 3. Ledger: the rows with item_kind quantity_item take quantity_item_id as
--    consumable_id and item_kind consumable. The append-only trigger is
--    disabled only for this statement (pattern 000074); RAISE NOTICE
--    reports the number of rewritten rows. Client and site of the rows stay
--    as derived at booking time. Afterwards the transitional column
--    quantity_item_id (with its foreign key and index) and the value
--    quantity_item of stock_movement_item_kind_check are gone;
--    stock_movement_object_check names asset_id and consumable_id, and
--    derive_stock_movement_scope loses its quantity_item branch.
-- 4. The register trigger barcode_registry_sync of consumable (000255) sees
--    the inserted rows; they carry no barcode, so no register row arises.

-- ─── 1. Articles ──────────────────────────────────────────────────────────────

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT q.organization_id, 'quantity_item', to_jsonb(q), 'sku_conflict', '000256'
  FROM quantity_item q
 WHERE q.sku IS NOT NULL
   AND EXISTS (SELECT 1 FROM consumable c WHERE c.organization_id = q.organization_id AND c.sku = q.sku);

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT q.organization_id, 'quantity_item', to_jsonb(q), 'attributes_dropped', '000256'
  FROM quantity_item q
 WHERE q.attributes IS NOT NULL AND q.attributes <> '{}'::jsonb;

INSERT INTO consumable (id, organization_id, client_id, sku, name, category, unit, min_level, notes, created_at, updated_at)
SELECT q.id, q.organization_id, q.client_id,
       CASE WHEN EXISTS (SELECT 1 FROM consumable c WHERE c.organization_id = q.organization_id AND c.sku = q.sku)
            THEN NULL ELSE q.sku END,
       q.name, q.category, q.unit, q.min_level, q.notes, q.created_at, q.updated_at
  FROM quantity_item q;

-- ─── 2. Stock ─────────────────────────────────────────────────────────────────

CREATE TEMPORARY VIEW quantity_item_stock AS
SELECT q.*,
       CASE WHEN q.stock_level < 0 THEN 'negative_stock'
            WHEN q.stock_level = 0 THEN NULL
            WHEN l.kind IS DISTINCT FROM 'bin' THEN 'stock_without_bin'
            WHEN l.client_id IS NULL OR (q.client_id IS NOT NULL AND q.client_id <> l.client_id) THEN 'bin_client_mismatch'
       END AS problem
  FROM quantity_item q
  LEFT JOIN location l ON l.id = q.location_id AND l.organization_id = q.organization_id;

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT s.organization_id, 'quantity_item', to_jsonb(q), s.problem, '000256'
  FROM quantity_item_stock s
  JOIN quantity_item q ON q.id = s.id
 WHERE s.problem IS NOT NULL;

INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand)
SELECT s.organization_id, s.id, s.location_id, s.stock_level
  FROM quantity_item_stock s
 WHERE s.problem IS NULL AND s.stock_level > 0;

DROP VIEW quantity_item_stock;

-- ─── 3. Ledger ────────────────────────────────────────────────────────────────

ALTER TABLE stock_movement DROP CONSTRAINT stock_movement_object_check,
    DROP CONSTRAINT stock_movement_item_kind_check;

ALTER TABLE stock_movement DISABLE TRIGGER stock_movement_append_only;
DO $$
DECLARE
    rewritten bigint;
BEGIN
    UPDATE stock_movement
       SET consumable_id = quantity_item_id, quantity_item_id = NULL, item_kind = 'consumable'
     WHERE item_kind = 'quantity_item';
    GET DIAGNOSTICS rewritten = ROW_COUNT;
    RAISE NOTICE 'stock_movement (000256): % quantity item movements rewritten to consumable', rewritten;
END
$$;
ALTER TABLE stock_movement ENABLE TRIGGER stock_movement_append_only;

DROP INDEX idx_stock_movement_quantity_item;
ALTER TABLE stock_movement DROP COLUMN quantity_item_id;

ALTER TABLE stock_movement
    ADD CONSTRAINT stock_movement_item_kind_check CHECK (item_kind IN ('asset', 'consumable')),
    ADD CONSTRAINT stock_movement_object_check CHECK (num_nonnulls(asset_id, consumable_id) = 1
        AND CASE item_kind
                WHEN 'asset' THEN asset_id IS NOT NULL
                ELSE consumable_id IS NOT NULL
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
