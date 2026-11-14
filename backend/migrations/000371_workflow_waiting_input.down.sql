-- Down of 000371: runs and steps that wait for a form are cancelled, then
-- the column run_id of form_submission and the status values go. The key
-- workflow_run_id_organization_key stays (earlier migrations reference it).

DROP INDEX IF EXISTS idx_form_submission_run;
ALTER TABLE form_submission DROP CONSTRAINT IF EXISTS form_submission_run_fkey;
ALTER TABLE form_submission DROP COLUMN IF EXISTS run_id;

UPDATE workflow_step SET status = 'cancelled', updated_at = now() WHERE status = 'waiting_input';
UPDATE workflow_run SET status = 'cancelled', finished_at = COALESCE(finished_at, now()), updated_at = now()
    WHERE status = 'waiting_input';

ALTER TABLE workflow_step DROP CONSTRAINT workflow_step_status_check;
ALTER TABLE workflow_step ADD CONSTRAINT workflow_step_status_check CHECK (status IN (
    'pending', 'running', 'waiting_approval', 'succeeded', 'failed', 'cancelled'));
ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_status_check;
ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_status_check CHECK (status IN (
    'pending', 'running', 'waiting_approval', 'succeeded', 'failed', 'cancelled'));
