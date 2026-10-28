-- Migration 000175: a reverted job records its revert (WP-553, API-09,
-- JOB-03; F-329).
--
-- POST /jobs/{id}/revert undid a succeeded job (the unmerge of ci.merge,
-- E-25) and left no trace on the job: it stayed succeeded, and a second
-- revert answered 409 with the unmerge's 'not found'. reverted_at and
-- reverted_by (a surrogate of the user like created_by, no reference)
-- record the revert; both are NULL for a job that was not reverted, and only
-- a succeeded job is reverted. The policies of migration 000154 (WP-404)
-- stay unchanged.

ALTER TABLE job
    ADD COLUMN reverted_at timestamptz,
    ADD COLUMN reverted_by uuid,
    ADD CONSTRAINT job_reverted_check CHECK (reverted_by IS NULL OR reverted_at IS NOT NULL),
    ADD CONSTRAINT job_reverted_status_check CHECK (reverted_at IS NULL OR status = 'succeeded');
