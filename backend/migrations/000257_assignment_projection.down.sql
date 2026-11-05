-- Reverts 000257_assignment_projection: the view assignment_projection, the
-- index idx_stock_movement_holder and the quarantine entries of the up
-- migration are removed. The movements it booked or replaced stay in the
-- append-only ledger; a new up finds the taken-over rows by their id.

DROP VIEW IF EXISTS assignment_projection;
DROP INDEX IF EXISTS idx_stock_movement_holder;
DELETE FROM migration_quarantine WHERE migration = '000257';
