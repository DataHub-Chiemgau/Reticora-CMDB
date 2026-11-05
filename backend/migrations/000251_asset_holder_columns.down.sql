-- Down of migration 000251: removes the due date of the ledger and the
-- holder cache of the asset in reverse order.

ALTER TABLE stock_movement DROP CONSTRAINT IF EXISTS stock_movement_due_at_assignment;
ALTER TABLE stock_movement DROP COLUMN IF EXISTS due_at;

DROP INDEX IF EXISTS idx_asset_assigned_team;
DROP INDEX IF EXISTS idx_asset_assigned_user;

ALTER TABLE asset
    DROP CONSTRAINT IF EXISTS asset_holder_due,
    DROP CONSTRAINT IF EXISTS asset_single_holder,
    DROP CONSTRAINT IF EXISTS asset_assigned_team_id_fkey,
    DROP CONSTRAINT IF EXISTS asset_assigned_user_id_fkey;

ALTER TABLE asset
    DROP COLUMN IF EXISTS assignment_due_at,
    DROP COLUMN IF EXISTS assigned_team_id,
    DROP COLUMN IF EXISTS assigned_user_id;
