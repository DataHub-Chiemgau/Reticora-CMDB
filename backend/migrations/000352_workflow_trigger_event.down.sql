-- Down of 000352.
ALTER TABLE event_outbox DROP COLUMN origin;
DROP INDEX IF EXISTS uq_workflow_run_trigger_event;
ALTER TABLE workflow_run DROP COLUMN trigger_depth, DROP COLUMN trigger_event_id;
