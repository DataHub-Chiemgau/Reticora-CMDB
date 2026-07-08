DROP INDEX IF EXISTS idx_user_invitation_email;
DROP INDEX IF EXISTS idx_user_invitation_org;
DROP INDEX IF EXISTS idx_api_key_prefix;
DROP INDEX IF EXISTS idx_api_key_org;

DROP POLICY IF EXISTS org_isolation ON user_invitation;
DROP POLICY IF EXISTS org_isolation ON api_key;

DROP TABLE IF EXISTS user_invitation;
DROP TABLE IF EXISTS api_key;
