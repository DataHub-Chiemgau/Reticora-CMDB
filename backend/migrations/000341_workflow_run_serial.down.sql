-- Reverts 000341: the serialization columns of workflow_run.

DROP INDEX idx_workflow_run_ci;
DROP INDEX idx_workflow_run_serial;
ALTER TABLE workflow_run
    DROP CONSTRAINT workflow_run_ci_fkey,
    DROP COLUMN enqueues,
    DROP COLUMN entity_seq,
    DROP COLUMN serial_key,
    DROP COLUMN ci_id;
