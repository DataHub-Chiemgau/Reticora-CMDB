ALTER TABLE IF EXISTS custom_role DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS team DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS custom_role_tenant_isolation ON custom_role;
DROP POLICY IF EXISTS team_tenant_isolation ON team;

ALTER TABLE IF EXISTS ticket DROP CONSTRAINT IF EXISTS ticket_team_id_fkey;

DROP INDEX IF EXISTS idx_user_custom_role_user;
DROP INDEX IF EXISTS idx_custom_role_org;
DROP INDEX IF EXISTS idx_team_member_user;
DROP INDEX IF EXISTS idx_team_member_team;
DROP INDEX IF EXISTS idx_team_org;
DROP INDEX IF EXISTS idx_custom_role_name_org;
DROP INDEX IF EXISTS idx_team_name_org;

DROP TABLE IF EXISTS user_custom_role;
DROP TABLE IF EXISTS custom_role;
DROP TABLE IF EXISTS team_member;
DROP TABLE IF EXISTS team;
