-- Reverts 000229_asset_ast01: drops the new columns, their references and
-- indexes, the barcode index and the currency trigger, restores the column
-- default EUR and gives the quarantined barcodes back. The supplier rows
-- stay (the table belongs to WP-269), as do the backfilled lifecycle
-- states and the locations taken from custom_fields (data, not schema).

DROP INDEX IF EXISTS idx_asset_barcode_org;

UPDATE asset a SET barcode = q.row_data ->> 'barcode'
FROM migration_quarantine q
WHERE q.migration = '000229' AND q.reason = 'duplicate_barcode' AND q.source_table = 'asset'
  AND a.id = (q.row_data ->> 'id')::uuid AND a.barcode IS NULL;

DELETE FROM migration_quarantine WHERE migration = '000229';

DROP TRIGGER IF EXISTS asset_default_currency ON asset;
DROP FUNCTION IF EXISTS asset_default_currency();
ALTER TABLE asset ALTER COLUMN currency SET DEFAULT 'EUR';

DROP INDEX IF EXISTS idx_asset_supplier;
DROP INDEX IF EXISTS idx_asset_contract;
DROP INDEX IF EXISTS idx_asset_owner_user;
DROP INDEX IF EXISTS idx_asset_owner_team;

ALTER TABLE asset
    DROP CONSTRAINT IF EXISTS asset_supplier_id_fkey,
    DROP CONSTRAINT IF EXISTS asset_contract_id_fkey,
    DROP CONSTRAINT IF EXISTS asset_owner_user_id_fkey,
    DROP CONSTRAINT IF EXISTS asset_owner_team_id_fkey;

ALTER TABLE asset
    DROP COLUMN IF EXISTS manufacturer,
    DROP COLUMN IF EXISTS model,
    DROP COLUMN IF EXISTS cost_center,
    DROP COLUMN IF EXISTS supplier_id,
    DROP COLUMN IF EXISTS contract_id,
    DROP COLUMN IF EXISTS owner_user_id,
    DROP COLUMN IF EXISTS owner_team_id;
