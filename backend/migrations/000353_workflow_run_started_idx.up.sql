-- Migration 000353: runs per hour of the workflow engine (WFL-08, WP-1112).
--
-- started_at is set when a run starts: in the request for a run executed
-- there, by the first claim of its job workflow.run for a queued run;
-- NULL while a queued run waits. Existing rows keep their value. The worker
-- counts the runs of an organization started in the last hour over
-- idx_workflow_run_org_started.

ALTER TABLE workflow_run ALTER COLUMN started_at DROP NOT NULL, ALTER COLUMN started_at DROP DEFAULT;
CREATE INDEX idx_workflow_run_org_started ON workflow_run (organization_id, started_at);
