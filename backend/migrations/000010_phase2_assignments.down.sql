ALTER TABLE IF EXISTS assignment DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS assignment_tenant_isolation ON assignment;

DROP INDEX IF EXISTS idx_assignment_status;
DROP INDEX IF EXISTS idx_assignment_asset;
DROP INDEX IF EXISTS idx_assignment_user;
DROP INDEX IF EXISTS idx_assignment_org;

DROP TABLE IF EXISTS assignment;
