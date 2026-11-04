-- Migration 000255 down: no barcode register and no location barcode
-- (WP-289). The barcodes of assets and consumables put into
-- migration_quarantine by the up migration are written back where no other
-- row of the same table took the code meanwhile (updated_at, version and the
-- history row of the asset stay, as in the up part); the codes of the
-- retired location nodes stay in location_node_retired. The quarantine
-- entries of 000255 are removed.

DROP TRIGGER IF EXISTS barcode_registry_sync ON location;
DROP TRIGGER IF EXISTS barcode_registry_sync ON consumable;
DROP TRIGGER IF EXISTS barcode_registry_sync ON asset;
DROP FUNCTION IF EXISTS barcode_registry_sync_location();
DROP FUNCTION IF EXISTS barcode_registry_sync_consumable();
DROP FUNCTION IF EXISTS barcode_registry_sync_asset();
DROP TABLE IF EXISTS barcode_registry;

ALTER TABLE location DROP COLUMN IF EXISTS barcode;

ALTER TABLE asset DISABLE TRIGGER trg_asset_updated_at;
ALTER TABLE asset DISABLE TRIGGER asset_version_bump;
ALTER TABLE asset DISABLE TRIGGER asset_history;
UPDATE asset a SET barcode = q.row_data ->> 'barcode'
  FROM migration_quarantine q
 WHERE q.migration = '000255' AND q.source_table = 'asset'
   AND q.organization_id = a.organization_id AND q.row_data ->> 'id' = a.id::text
   AND a.barcode IS NULL
   AND NOT EXISTS (SELECT 1 FROM asset o
                    WHERE o.organization_id = a.organization_id AND o.barcode = q.row_data ->> 'barcode');
ALTER TABLE asset ENABLE TRIGGER asset_history;
ALTER TABLE asset ENABLE TRIGGER asset_version_bump;
ALTER TABLE asset ENABLE TRIGGER trg_asset_updated_at;

UPDATE consumable c SET barcode = q.row_data ->> 'barcode'
  FROM migration_quarantine q
 WHERE q.migration = '000255' AND q.source_table = 'consumable'
   AND q.organization_id = c.organization_id AND q.row_data ->> 'id' = c.id::text
   AND c.barcode IS NULL
   AND NOT EXISTS (SELECT 1 FROM consumable o
                    WHERE o.organization_id = c.organization_id AND o.barcode = q.row_data ->> 'barcode');

DELETE FROM migration_quarantine WHERE migration = '000255';
