ALTER TABLE IF EXISTS stock_scan DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS stocktake DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS stock_scan_tenant_isolation ON stock_scan;
DROP POLICY IF EXISTS stocktake_tenant_isolation ON stocktake;

DROP INDEX IF EXISTS idx_stock_scan_asset;
DROP INDEX IF EXISTS idx_stock_scan_stocktake;
DROP INDEX IF EXISTS idx_stocktake_status;
DROP INDEX IF EXISTS idx_stocktake_org;

DROP TABLE IF EXISTS stock_scan;
DROP TABLE IF EXISTS stocktake;
