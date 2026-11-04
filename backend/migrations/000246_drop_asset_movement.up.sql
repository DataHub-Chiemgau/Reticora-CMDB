-- Migration 000246: drop the legacy ledger asset_movement (WP-842, AST-07).
--
-- Since 000242 (WP-744) no path reads or writes asset_movement; its rows
-- stand with the same id in the ledger stock_movement. Rows 000242 kept out
-- of the ledger are in migration_quarantine (migration '000242',
-- source_table 'asset_movement', the whole row in row_data, E-26).
--
-- 1. Every row of asset_movement must be in stock_movement with the same id
--    or quarantined by 000242. Otherwise the migration aborts with the
--    number of missing rows and changes nothing.
-- 2. asset_movement is dropped with its triggers, policy and foreign keys,
--    then the trigger function reject_asset_movement_mutation().

DO $$
DECLARE
    missing bigint;
BEGIN
    SELECT count(*) INTO missing
      FROM asset_movement a
     WHERE NOT EXISTS (SELECT 1 FROM stock_movement s WHERE s.id = a.id)
       AND NOT EXISTS (SELECT 1 FROM migration_quarantine q
                        WHERE q.migration = '000242' AND q.source_table = 'asset_movement'
                          AND q.row_data ->> 'id' = a.id::text);
    IF missing > 0 THEN
        RAISE EXCEPTION 'asset_movement (000246): % rows are missing in stock_movement', missing;
    END IF;
END
$$;

DROP TABLE asset_movement;
DROP FUNCTION reject_asset_movement_mutation();
