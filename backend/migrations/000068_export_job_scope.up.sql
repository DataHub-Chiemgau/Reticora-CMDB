-- WP-035 (EXP-01, TEN-06): export jobs run with the scope of their creator.
--
-- scope_clients/scope_sites/scope_teams snapshot the creator's tenant scope
-- when the job is queued: NULL grants the whole organization in that
-- dimension, an array restricts it to the listed ids (empty: none). The
-- worker renders the export in database.WithTenant with exactly this scope.
-- scope_recorded marks jobs that carry a snapshot; queued jobs from before
-- this migration have none and are failed instead of being exported
-- org-wide.

ALTER TABLE export_job
    ADD COLUMN scope_recorded BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN scope_clients UUID[],
    ADD COLUMN scope_sites UUID[],
    ADD COLUMN scope_teams UUID[];

UPDATE export_job
   SET status = 'failed',
       error_message = 'export queued without a scope snapshot; create the export again',
       completed_at = now()
 WHERE status IN ('pending', 'running');

CREATE INDEX idx_export_job_initiator ON export_job (organization_id, initiated_by, created_at DESC);
