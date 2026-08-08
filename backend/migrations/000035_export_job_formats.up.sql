-- Align the export_job format constraint with the formats the export
-- pipeline actually renders (csv, json, datev). The original constraint
-- predates the DATEV implementation and listed xlsx, which no code path
-- produces.

ALTER TABLE export_job DROP CONSTRAINT chk_export_format;
ALTER TABLE export_job ADD CONSTRAINT chk_export_format
    CHECK (format IN ('csv', 'json', 'datev'));

-- The export worker claims pending jobs across tenants with the app.system
-- flag, mirroring the webhook_delivery exception. Request paths always run
-- with app.org_id and keep tenant isolation.
DROP POLICY IF EXISTS org_isolation ON export_job;
CREATE POLICY org_isolation ON export_job
    USING (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    )
    WITH CHECK (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    );

CREATE INDEX IF NOT EXISTS idx_export_job_pending
    ON export_job (created_at ASC, id ASC)
    WHERE status = 'pending';
