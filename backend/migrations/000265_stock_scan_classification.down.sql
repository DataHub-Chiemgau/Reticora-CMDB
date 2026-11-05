-- Migration 000265 down: scans without classification against the snapshot
-- and without scope columns (WP-287). scan_result returns from
-- legacy_scan_result; a row recorded since then keeps found, unexpected
-- without original value becomes surplus. Scans of consumables keep their
-- row without consumable and quantity. The organization policy of
-- 000012/000023 returns.

DROP POLICY IF EXISTS stock_scan_scoped_delete ON stock_scan;
DROP POLICY IF EXISTS stock_scan_scoped_update ON stock_scan;
DROP POLICY IF EXISTS stock_scan_isolation ON stock_scan;
CREATE POLICY stock_scan_tenant_isolation ON stock_scan
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER IF EXISTS stocktake_refresh_scans ON stocktake;
DROP FUNCTION IF EXISTS stocktake_refresh_scans();
DROP TRIGGER IF EXISTS stock_scan_derive_scope ON stock_scan;
DROP FUNCTION IF EXISTS stock_scan_derive_scope();

ALTER TABLE stock_scan DROP CONSTRAINT scan_result_check;
UPDATE stock_scan SET scan_result = CASE
        WHEN legacy_scan_result IS NOT NULL THEN legacy_scan_result
        WHEN scan_result = 'unexpected' THEN 'surplus'
        ELSE scan_result
    END;
ALTER TABLE stock_scan ADD CONSTRAINT scan_result_check CHECK (scan_result IN (
    'found', 'missing', 'surplus', 'damaged', 'wrong_location'));

DROP INDEX IF EXISTS idx_stock_scan_stocktake_scanned;
DROP INDEX IF EXISTS idx_stock_scan_client;
DROP INDEX IF EXISTS idx_stock_scan_consumable;
ALTER TABLE stock_scan DROP CONSTRAINT IF EXISTS stock_scan_quantity_check,
    DROP CONSTRAINT IF EXISTS stock_scan_consumable_fkey,
    DROP COLUMN IF EXISTS site_id,
    DROP COLUMN IF EXISTS client_id,
    DROP COLUMN IF EXISTS legacy_scan_result,
    DROP COLUMN IF EXISTS code,
    DROP COLUMN IF EXISTS quantity,
    DROP COLUMN IF EXISTS consumable_id;
