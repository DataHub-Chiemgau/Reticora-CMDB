-- Reverts WP-035: drops the scope snapshot. Jobs failed by the up migration
-- stay failed.

DROP INDEX IF EXISTS idx_export_job_initiator;
ALTER TABLE export_job
    DROP COLUMN IF EXISTS scope_teams,
    DROP COLUMN IF EXISTS scope_sites,
    DROP COLUMN IF EXISTS scope_clients,
    DROP COLUMN IF EXISTS scope_recorded;
