-- Down of 000267: the result of a stocktake is writable again.
DROP TRIGGER IF EXISTS stocktake_item_immutable ON stocktake_item;
DROP TRIGGER IF EXISTS stock_scan_immutable ON stock_scan;
DROP TRIGGER IF EXISTS stocktake_immutable ON stocktake;
DROP FUNCTION IF EXISTS reject_completed_stocktake_mutation();
