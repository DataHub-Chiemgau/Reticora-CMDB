-- Reverts 000361: the deadline columns of workflow_step, the table
-- workflow_approval with its triggers and functions; the keys
-- workflow_run_id_organization_key and workflow_step_id_organization_key go
-- only when nothing references them any more (workflow_step, workflow_run
-- and iga_access_request reference the one of workflow_run since 000288).

DROP INDEX idx_workflow_step_due;
ALTER TABLE workflow_step DROP COLUMN reminded_at, DROP COLUMN due_at;

DROP TRIGGER workflow_approval_scope_propagation ON workflow_run;
DROP TABLE workflow_approval;
DROP FUNCTION propagate_workflow_approval_scope();
DROP FUNCTION workflow_approval_scope();

-- workflow-run-step-key:begin
DO $$
DECLARE
    t text;
    key_index oid;
BEGIN
    FOREACH t IN ARRAY ARRAY['workflow_step', 'workflow_run'] LOOP
        SELECT conindid INTO key_index FROM pg_constraint
         WHERE conrelid = t::regclass AND conname = t || '_id_organization_key';
        IF key_index IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM pg_constraint f WHERE f.contype = 'f' AND f.confrelid = t::regclass AND f.conindid = key_index
        ) THEN
            EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', t, t || '_id_organization_key');
        END IF;
    END LOOP;
END
$$;
-- workflow-run-step-key:end
