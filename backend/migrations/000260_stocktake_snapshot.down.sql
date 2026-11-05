-- Migration 000260 down: stocktake without area and snapshot (WP-848). The
-- snapshot rows go with stocktake_item; status, scope and the counters of
-- the stocktakes stay. The organization policy of 000012/000023 returns.

DROP POLICY IF EXISTS stocktake_scoped_delete ON stocktake;
DROP POLICY IF EXISTS stocktake_scoped_update ON stocktake;
DROP POLICY IF EXISTS stocktake_isolation ON stocktake;
CREATE POLICY stocktake_tenant_isolation ON stocktake
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP INDEX IF EXISTS idx_stock_scan_location;
ALTER TABLE stock_scan DROP CONSTRAINT IF EXISTS stock_scan_location_fkey,
    DROP COLUMN IF EXISTS location_id;

DROP TRIGGER IF EXISTS stocktake_refresh_items ON stocktake;
DROP FUNCTION IF EXISTS stocktake_refresh_items();
DROP TABLE IF EXISTS stocktake_item;
DROP FUNCTION IF EXISTS stocktake_item_derive_scope();

DROP TRIGGER IF EXISTS location_refresh_stocktake ON location;
DROP FUNCTION IF EXISTS location_refresh_stocktake();
DROP TRIGGER IF EXISTS stocktake_derive_location ON stocktake;
DROP FUNCTION IF EXISTS stocktake_derive_location();

DROP INDEX IF EXISTS idx_stocktake_client;
DROP INDEX IF EXISTS idx_stocktake_location;
ALTER TABLE stocktake DROP CONSTRAINT IF EXISTS stocktake_location_fkey,
    DROP COLUMN IF EXISTS site_id,
    DROP COLUMN IF EXISTS client_id,
    DROP COLUMN IF EXISTS location_id,
    DROP CONSTRAINT IF EXISTS stocktake_id_organization_key;
