-- Migration 000023: unify the RLS session variable and retire legacy CI columns
--
-- Three different session variable names had accumulated across migrations
-- (app.organization_id, app.current_org, app.org_id) while every repository sets
-- app.org_id. Any table whose policy referenced one of the other two raised
-- "unrecognized configuration parameter" at query time, so this migration
-- rewrites every tenant policy onto app.org_id, adds the missing WITH CHECK
-- clauses and FORCE ROW LEVEL SECURITY, and finally drops the duplicated
-- discovery columns on ci (see S1).

-- ─── 1. Unified tenant policies ─────────────────────────────────────────────────

-- Tables keyed on their own id.
DROP POLICY IF EXISTS org_isolation ON organization;
CREATE POLICY org_isolation ON organization
    USING (id = current_setting('app.org_id')::UUID)
    WITH CHECK (id = current_setting('app.org_id')::UUID);

-- Tables keyed on organization_id. The DO block keeps the policy name that each
-- table already used so repeated applies stay idempotent.
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
            ('rack', 'rack_isolation'),
            ('ci_type', 'ci_type_isolation'),
            ('ci', 'ci_isolation'),
            ('audit_log', 'audit_isolation'),
            ('entitlement', 'entitlement_isolation'),
            ('app_user', 'user_isolation'),
            ('role', 'role_isolation'),
            ('role_assignment', 'role_assignment_isolation'),
            ('ci_relationship', 'ci_relationship_isolation'),
            ('webhook_subscription', 'webhook_sub_isolation'),
            ('webhook_delivery', 'webhook_delivery_isolation'),
            ('collector', 'collector_isolation'),
            ('discovery_job', 'discovery_job_isolation'),
            ('discovery_result', 'discovery_result_isolation'),
            ('asset', 'asset_tenant_isolation'),
            ('assignment', 'assignment_tenant_isolation'),
            ('document', 'document_tenant_isolation'),
            ('document_link', 'document_link_tenant_isolation'),
            ('stocktake', 'stocktake_tenant_isolation'),
            ('stock_scan', 'stock_scan_tenant_isolation'),
            ('ticket', 'ticket_tenant_isolation'),
            ('ticket_comment', 'ticket_comment_tenant_isolation'),
            ('team', 'team_tenant_isolation'),
            ('custom_role', 'custom_role_tenant_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', entry.table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', entry.table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (organization_id = current_setting(''app.org_id'')::UUID) '
            'WITH CHECK (organization_id = current_setting(''app.org_id'')::UUID)',
            entry.policy_name, entry.table_name
        );
    END LOOP;
END
$$;

-- ─── 2. Retire the duplicated CI discovery columns (S1) ─────────────────────────

UPDATE ci
SET last_seen_at = COALESCE(last_seen_at, last_seen)
WHERE last_seen IS NOT NULL AND last_seen_at IS NULL;

UPDATE ci
SET first_seen_at = COALESCE(first_seen_at, last_seen, created_at)
WHERE first_seen_at IS NULL;

-- The legacy `source` column used a coarser vocabulary than discovery_source.
UPDATE ci
SET discovery_source = CASE
        WHEN source IN ('snmp', 'ssh', 'redfish', 'ipmi', 'wmi', 'api', 'agent', 'sweep', 'manual') THEN source
        WHEN source = 'discovery' THEN 'sweep'
        ELSE 'manual'
    END
WHERE discovery_source IS NULL AND source IS NOT NULL;

DROP INDEX IF EXISTS idx_ci_last_seen;
ALTER TABLE ci DROP COLUMN IF EXISTS last_seen;
ALTER TABLE ci DROP COLUMN IF EXISTS source;

CREATE INDEX IF NOT EXISTS idx_ci_last_seen_at ON ci(last_seen_at);

-- ─── 3. Identity-resolution and lifecycle indexes (S5) ──────────────────────────

CREATE INDEX IF NOT EXISTS idx_ci_hostname ON ci(organization_id, lower(hostname))
    WHERE hostname IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ci_fqdn ON ci(organization_id, lower(fqdn))
    WHERE fqdn IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ci_mgmt_ip_sys_object ON ci(organization_id, management_ip, sys_object_id)
    WHERE management_ip IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ci_org_serial ON ci(organization_id, serial_number)
    WHERE serial_number IS NOT NULL AND serial_number <> '' AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ci_org_hardware_uuid ON ci(organization_id, hardware_uuid)
    WHERE hardware_uuid IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ci_org_primary_mac ON ci(organization_id, primary_mac)
    WHERE primary_mac IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_api_key_active ON api_key(organization_id, key_prefix)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_invitation_pending ON user_invitation(organization_id, email)
    WHERE accepted_at IS NULL;
