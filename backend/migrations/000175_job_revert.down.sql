-- Migration 000175 down: jobs no longer record their revert (state of
-- migration 000154).

ALTER TABLE job
    DROP CONSTRAINT job_reverted_status_check,
    DROP CONSTRAINT job_reverted_check,
    DROP COLUMN reverted_by,
    DROP COLUMN reverted_at;
