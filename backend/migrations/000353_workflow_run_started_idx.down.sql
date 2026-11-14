-- Down of 000353.
DROP INDEX IF EXISTS idx_workflow_run_org_started;
UPDATE workflow_run SET started_at = created_at WHERE started_at IS NULL;
ALTER TABLE workflow_run ALTER COLUMN started_at SET DEFAULT now(), ALTER COLUMN started_at SET NOT NULL;
