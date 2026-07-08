ALTER TABLE IF EXISTS discovery_result DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS discovery_job DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS collector DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS discovery_result_isolation ON discovery_result;
DROP POLICY IF EXISTS discovery_job_isolation ON discovery_job;
DROP POLICY IF EXISTS collector_isolation ON collector;

DROP INDEX IF EXISTS idx_discovery_result_ci;
DROP INDEX IF EXISTS idx_discovery_result_job;
DROP INDEX IF EXISTS idx_discovery_job_status;
DROP INDEX IF EXISTS idx_discovery_job_collector;
DROP INDEX IF EXISTS idx_collector_org;

DROP TABLE IF EXISTS discovery_result;
DROP TABLE IF EXISTS discovery_job;
DROP TABLE IF EXISTS collector;
