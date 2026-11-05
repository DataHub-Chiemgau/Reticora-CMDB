-- Migration 000252 down: stock of consumables back on the consumable row
-- (WP-283). stock_level is the sum of on_hand over the bins; a consumable
-- without stock_level row gets the value of its quarantine entry, location
-- the text of it. The quarantine entries of 000252 are removed.

ALTER TABLE consumable ADD COLUMN stock_level NUMERIC(12,2) NOT NULL DEFAULT 0,
    ADD COLUMN location TEXT;

UPDATE consumable c SET stock_level = s.total
  FROM (SELECT consumable_id, sum(on_hand) AS total FROM stock_level GROUP BY consumable_id) s
 WHERE s.consumable_id = c.id;

UPDATE consumable c
   SET stock_level = CASE WHEN EXISTS (SELECT 1 FROM stock_level s WHERE s.consumable_id = c.id)
                          THEN c.stock_level
                          ELSE COALESCE((q.row_data ->> 'stock_level')::numeric, 0) END,
       location = q.row_data ->> 'location'
  FROM migration_quarantine q
 WHERE q.migration = '000252' AND q.source_table = 'consumable'
   AND q.organization_id = c.organization_id AND q.row_data ->> 'id' = c.id::text;

DELETE FROM migration_quarantine WHERE migration = '000252';

DROP TRIGGER IF EXISTS consumable_client_stock ON consumable;
DROP FUNCTION IF EXISTS consumable_client_stock();
DROP TRIGGER IF EXISTS location_refresh_stock_level ON location;
DROP FUNCTION IF EXISTS location_refresh_stock_level();
DROP TABLE IF EXISTS stock_level;
DROP FUNCTION IF EXISTS stock_level_derive_location();

DROP INDEX IF EXISTS idx_consumable_supplier;
DROP INDEX IF EXISTS consumable_organization_id_barcode_key;
ALTER TABLE consumable DROP CONSTRAINT IF EXISTS consumable_supplier_id_fkey,
    DROP COLUMN IF EXISTS supplier_id,
    DROP COLUMN IF EXISTS barcode;
