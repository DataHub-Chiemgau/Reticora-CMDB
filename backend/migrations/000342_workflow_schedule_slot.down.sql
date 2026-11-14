-- Reverts 000342: the schedule slot of workflow_run.

DROP INDEX uq_workflow_run_schedule_slot;
ALTER TABLE workflow_run DROP COLUMN schedule_slot;
