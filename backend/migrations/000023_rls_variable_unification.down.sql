-- Revert migration 000023: restore the legacy CI columns and session variables.

-- ─── 3. Drop the indexes added by the up migration ──────────────────────────────
DROP INDEX IF EXISTS idx_user_invitation_pending;
DROP INDEX IF EXISTS idx_api_key_active;
DROP INDEX IF EXISTS uq_ci_org_primary_mac;
DROP INDEX IF EXISTS uq_ci_org_hardware_uuid;
DROP INDEX IF EXISTS uq_ci_org_serial;
DROP INDEX IF EXISTS idx_ci_mgmt_ip_sys_object;
DROP INDEX IF EXISTS idx_ci_fqdn;
DROP INDEX IF EXISTS idx_ci_hostname;

-- ─── 2. Restore the legacy CI columns and backfill them from the new ones ───────
ALTER TABLE ci ADD COLUMN IF NOT EXISTS source TEXT;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS last_seen TIMESTAMPTZ;

UPDATE ci SET last_seen = last_seen_at WHERE last_seen_at IS NOT NULL;
UPDATE ci SET source = discovery_source WHERE discovery_source IS NOT NULL;

DROP INDEX IF EXISTS idx_ci_last_seen_at;
CREATE INDEX IF NOT EXISTS idx_ci_last_seen ON ci(last_seen);

-- ─── 1. Restore the pre-0023 policy definitions ─────────────────────────────────
DROP POLICY IF EXISTS org_isolation ON organization;
CREATE POLICY org_isolation ON organization
    USING (id = current_setting('app.org_id')::UUID)
    WITH CHECK (id = current_setting('app.org_id')::UUID);

DO $$
DECLARE
    entry RECORD;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('ci_type', 'ci_type_isolation', 'app.organization_id'),
            ('ci', 'ci_isolation', 'app.organization_id'),
            ('audit_log', 'audit_isolation', 'app.organization_id'),
            ('entitlement', 'entitlement_isolation', 'app.organization_id'),
            ('app_user', 'user_isolation', 'app.organization_id'),
            ('role', 'role_isolation', 'app.organization_id'),
            ('role_assignment', 'role_assignment_isolation', 'app.organization_id'),
            ('ci_relationship', 'ci_relationship_isolation', 'app.organization_id'),
            ('webhook_subscription', 'webhook_sub_isolation', 'app.organization_id'),
            ('webhook_delivery', 'webhook_delivery_isolation', 'app.organization_id'),
            ('collector', 'collector_isolation', 'app.organization_id'),
            ('discovery_job', 'discovery_job_isolation', 'app.organization_id'),
            ('discovery_result', 'discovery_result_isolation', 'app.organization_id'),
            ('asset', 'asset_tenant_isolation', 'app.current_org'),
            ('assignment', 'assignment_tenant_isolation', 'app.current_org'),
            ('document', 'document_tenant_isolation', 'app.current_org'),
            ('document_link', 'document_link_tenant_isolation', 'app.current_org'),
            ('stocktake', 'stocktake_tenant_isolation', 'app.current_org'),
            ('stock_scan', 'stock_scan_tenant_isolation', 'app.current_org'),
            ('ticket', 'ticket_tenant_isolation', 'app.current_org'),
            ('ticket_comment', 'ticket_comment_tenant_isolation', 'app.current_org'),
            ('team', 'team_tenant_isolation', 'app.current_org'),
            ('custom_role', 'custom_role_tenant_isolation', 'app.current_org')
        ) AS t(table_name, policy_name, setting_name)
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (organization_id = current_setting(%L)::UUID)',
            entry.policy_name, entry.table_name, entry.setting_name
        );
    END LOOP;
END
$$;

-- The location tables already had app.org_id with WITH CHECK since 000018.
DO $$
DECLARE
    entry RECORD;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('client', 'client_isolation'),
            ('site', 'site_isolation'),
            ('building', 'building_isolation'),
            ('room', 'room_isolation'),
            ('rack', 'rack_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (organization_id = current_setting(''app.org_id'')::UUID) '
            'WITH CHECK (organization_id = current_setting(''app.org_id'')::UUID)',
            entry.policy_name, entry.table_name
        );
    END LOOP;
END
$$;

-- Tables that only got FORCE ROW LEVEL SECURITY from the up migration.
ALTER TABLE asset NO FORCE ROW LEVEL SECURITY;
ALTER TABLE assignment NO FORCE ROW LEVEL SECURITY;
ALTER TABLE custom_role NO FORCE ROW LEVEL SECURITY;
ALTER TABLE discovery_result NO FORCE ROW LEVEL SECURITY;
ALTER TABLE document NO FORCE ROW LEVEL SECURITY;
ALTER TABLE document_link NO FORCE ROW LEVEL SECURITY;
ALTER TABLE stock_scan NO FORCE ROW LEVEL SECURITY;
ALTER TABLE stocktake NO FORCE ROW LEVEL SECURITY;
ALTER TABLE team NO FORCE ROW LEVEL SECURITY;
ALTER TABLE ticket NO FORCE ROW LEVEL SECURITY;
ALTER TABLE ticket_comment NO FORCE ROW LEVEL SECURITY;
