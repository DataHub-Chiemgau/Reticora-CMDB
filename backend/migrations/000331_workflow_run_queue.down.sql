-- Reverts 000331: the job columns of workflow_run.

DROP INDEX idx_workflow_run_initiated_by;
ALTER TABLE workflow_run
    DROP CONSTRAINT workflow_run_initiated_by_fkey,
    DROP COLUMN initiated_by,
    DROP COLUMN last_error,
    DROP COLUMN next_attempt_at,
    DROP COLUMN attempts;
