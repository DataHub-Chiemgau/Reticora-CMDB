-- Reverts 000317: workflow_run.definition_version_id, workflow_def.version
-- and current_version_id, the table workflow_def_version with its triggers
-- and function; the key workflow_def_id_organization_key goes only when
-- nothing references it any more (workflow_run references it since 000288).

DROP INDEX idx_workflow_run_definition_version;
ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_definition_version_fkey;
ALTER TABLE workflow_run DROP COLUMN definition_version_id;

ALTER TABLE workflow_def DROP CONSTRAINT workflow_def_current_version_fkey;
ALTER TABLE workflow_def DROP COLUMN current_version_id;
ALTER TABLE workflow_def DROP COLUMN version;

DROP TABLE workflow_def_version;
DROP FUNCTION workflow_def_version_immutable();

-- workflow-def-key:begin
DO $$
DECLARE
    key_index oid;
BEGIN
    SELECT conindid INTO key_index FROM pg_constraint
     WHERE conrelid = 'workflow_def'::regclass AND conname = 'workflow_def_id_organization_key';
    IF key_index IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f WHERE f.contype = 'f' AND f.confrelid = 'workflow_def'::regclass AND f.conindid = key_index
    ) THEN
        ALTER TABLE workflow_def DROP CONSTRAINT workflow_def_id_organization_key;
    END IF;
END
$$;
-- workflow-def-key:end
