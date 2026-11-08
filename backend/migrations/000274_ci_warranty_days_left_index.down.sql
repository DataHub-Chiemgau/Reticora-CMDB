-- Down of 000274: the filter on warranty_days_left reads without an index
-- again; dropping the column drops idx_ci_warranty_days_left with it.
DROP INDEX IF EXISTS idx_ci_warranty_days_left;

ALTER TABLE ci DROP COLUMN warranty_days_left_num;
