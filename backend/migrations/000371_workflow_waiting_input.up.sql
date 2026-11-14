-- Migration 000371: forms as trigger and step of workflows (WFL-01, WFL-02,
-- WP-1102).
--
-- 1. workflow_run_status_check and workflow_step_status_check admit
--    waiting_input: a run (and its step) of the action request_form waits
--    for the submission of a form.
-- 2. workflow_run gets UNIQUE (id, organization_id) when it lacks it
--    (workflow_run_id_organization_key, convention 19); the down leaves
--    the key, which earlier migrations reference as well.
-- 3. form_submission.run_id names the run a submission continued: a
--    composite foreign key on workflow_run (ON DELETE SET NULL (run_id))
--    and an index (organization_id, run_id). A run takes one submission
--    per waiting request_form step, so a run with several such steps has
--    several: the lock on run and step and the status check of the
--    continuation refuse a second submission to the same step, no unique
--    index.
--
-- No new table; the row level security of form_submission (WP-1093) is
-- unchanged.

ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_status_check;
ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_status_check CHECK (status IN (
    'pending', 'running', 'waiting_approval', 'waiting_input', 'succeeded', 'failed', 'cancelled'));
ALTER TABLE workflow_step DROP CONSTRAINT workflow_step_status_check;
ALTER TABLE workflow_step ADD CONSTRAINT workflow_step_status_check CHECK (status IN (
    'pending', 'running', 'waiting_approval', 'waiting_input', 'succeeded', 'failed', 'cancelled'));

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workflow_run_id_organization_key'
                   AND conrelid = 'workflow_run'::regclass) THEN
        ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_id_organization_key UNIQUE (id, organization_id);
    END IF;
END $$;

ALTER TABLE form_submission ADD COLUMN run_id UUID NULL;
ALTER TABLE form_submission ADD CONSTRAINT form_submission_run_fkey
    FOREIGN KEY (run_id, organization_id) REFERENCES workflow_run (id, organization_id) ON DELETE SET NULL (run_id);
CREATE INDEX idx_form_submission_run ON form_submission (organization_id, run_id);
