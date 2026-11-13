DROP INDEX IF EXISTS idx_probe_suspended;
ALTER TABLE probe DROP COLUMN IF EXISTS suspended_at, DROP COLUMN IF EXISTS suspended_reason;
