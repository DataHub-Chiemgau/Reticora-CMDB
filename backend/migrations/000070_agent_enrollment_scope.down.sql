-- Reverts WP-038: organization-only agent policy, no enrollment tokens.

DROP POLICY endpoint_agent_isolation ON endpoint_agent;
CREATE POLICY endpoint_agent_isolation ON endpoint_agent
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP INDEX IF EXISTS idx_endpoint_agent_client;
ALTER TABLE endpoint_agent
    DROP COLUMN IF EXISTS site_confirmed_at,
    DROP COLUMN IF EXISTS network_fingerprint,
    DROP COLUMN IF EXISTS suggested_site_id,
    DROP COLUMN IF EXISTS site_id,
    DROP COLUMN IF EXISTS client_id;

DROP TABLE IF EXISTS agent_enrollment_token;
DROP FUNCTION IF EXISTS agent_enrollment_token_site_check();
