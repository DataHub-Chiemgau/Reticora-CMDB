DROP INDEX IF EXISTS idx_export_job_status;
DROP INDEX IF EXISTS idx_export_job_org;

DROP POLICY IF EXISTS org_isolation ON export_job;

DROP TABLE IF EXISTS export_job;
