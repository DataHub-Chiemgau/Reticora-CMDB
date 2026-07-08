ALTER TABLE IF EXISTS audit_log DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS audit_isolation ON audit_log;

DROP INDEX IF EXISTS idx_audit_actor;
DROP INDEX IF EXISTS idx_audit_resource;
DROP INDEX IF EXISTS idx_audit_timestamp;
DROP INDEX IF EXISTS idx_audit_org;

DROP TABLE IF EXISTS audit_log;
