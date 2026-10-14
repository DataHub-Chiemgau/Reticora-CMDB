-- Migration 000133 down: no sort, columns or role sharing of saved views.
DROP INDEX IF EXISTS idx_saved_view_shared_roles;
ALTER TABLE saved_view DROP COLUMN shared_with_roles, DROP COLUMN columns, DROP COLUMN sort;
