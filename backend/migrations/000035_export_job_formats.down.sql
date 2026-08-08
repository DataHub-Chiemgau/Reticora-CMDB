DROP INDEX IF EXISTS idx_export_job_pending;

DROP POLICY IF EXISTS org_isolation ON export_job;
CREATE POLICY org_isolation ON export_job
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE export_job DROP CONSTRAINT chk_export_format;
ALTER TABLE export_job ADD CONSTRAINT chk_export_format
    CHECK (format IN ('csv', 'json', 'xlsx'));
