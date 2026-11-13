-- Down of 000351: the policies of 000027 over the organization only, without
-- the scope columns, their triggers and indexes.

DROP POLICY IF EXISTS workflow_run_tenant_isolation ON workflow_run;
DROP POLICY IF EXISTS workflow_run_scoped_update ON workflow_run;
DROP POLICY IF EXISTS workflow_run_scoped_delete ON workflow_run;
DROP POLICY IF EXISTS workflow_step_tenant_isolation ON workflow_step;
DROP POLICY IF EXISTS workflow_step_scoped_update ON workflow_step;
DROP POLICY IF EXISTS workflow_step_scoped_delete ON workflow_step;
DROP POLICY IF EXISTS form_submission_tenant_isolation ON form_submission;
DROP POLICY IF EXISTS form_submission_scoped_update ON form_submission;
DROP POLICY IF EXISTS form_submission_scoped_delete ON form_submission;
CREATE POLICY workflow_run_tenant_isolation ON workflow_run USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY workflow_step_tenant_isolation ON workflow_step USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY form_submission_tenant_isolation ON form_submission USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP INDEX IF EXISTS idx_workflow_run_created;
DROP INDEX IF EXISTS idx_form_submission_site;
DROP INDEX IF EXISTS idx_form_submission_client;
DROP INDEX IF EXISTS idx_workflow_step_site;
DROP INDEX IF EXISTS idx_workflow_step_client;
DROP INDEX IF EXISTS idx_workflow_run_site;
DROP INDEX IF EXISTS idx_workflow_run_client;

DROP TRIGGER IF EXISTS workflow_step_scope_propagation ON workflow_run;
DROP TRIGGER IF EXISTS form_submission_scope_propagation ON form_def;
DROP TRIGGER IF EXISTS workflow_scope_propagation ON ci;
DROP TRIGGER IF EXISTS form_submission_scope ON form_submission;
DROP TRIGGER IF EXISTS workflow_step_scope ON workflow_step;
DROP TRIGGER IF EXISTS workflow_run_scope ON workflow_run;
DROP FUNCTION IF EXISTS propagate_workflow_step_scope();
DROP FUNCTION IF EXISTS propagate_submission_scope_from_form();
DROP FUNCTION IF EXISTS propagate_workflow_scope_from_ci();
DROP FUNCTION IF EXISTS form_submission_scope();
DROP FUNCTION IF EXISTS workflow_step_scope();
DROP FUNCTION IF EXISTS workflow_run_scope();
DROP FUNCTION IF EXISTS app_scope_single(text);

ALTER TABLE form_submission DROP COLUMN site_id, DROP COLUMN client_id;
ALTER TABLE workflow_step DROP COLUMN site_id, DROP COLUMN client_id;
ALTER TABLE workflow_run DROP COLUMN site_id, DROP COLUMN client_id;
