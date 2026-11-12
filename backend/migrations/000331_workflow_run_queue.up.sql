-- Migration 000331: a workflow run is executed as the job workflow.run
-- with retries (WFL-02, WP-1088, E-226 option 2).
--
-- workflow_run gets
--   attempts         the attempts of the job that executed the run (0: run
--                    in the request),
--   next_attempt_at  when the job retries the run after a failed attempt,
--   last_error       the sanitized error of the last failed attempt,
--   initiated_by     the user who started the run; the worker executes the
--                    run as this user. Composite foreign key with the
--                    organization (Konvention 19), ON DELETE SET NULL of the
--                    user column only.
-- Policies are unchanged (organization of the run).

ALTER TABLE workflow_run
    ADD COLUMN attempts integer NOT NULL DEFAULT 0 CONSTRAINT workflow_run_attempts_check CHECK (attempts >= 0),
    ADD COLUMN next_attempt_at timestamptz NULL,
    ADD COLUMN last_error text NULL,
    ADD COLUMN initiated_by uuid NULL,
    ADD CONSTRAINT workflow_run_initiated_by_fkey FOREIGN KEY (initiated_by, organization_id)
        REFERENCES app_user (id, organization_id) ON DELETE SET NULL (initiated_by);
CREATE INDEX idx_workflow_run_initiated_by ON workflow_run (organization_id, initiated_by) WHERE initiated_by IS NOT NULL;
