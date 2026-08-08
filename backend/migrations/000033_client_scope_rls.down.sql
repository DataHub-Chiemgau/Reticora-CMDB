-- Migration 000033 (down): restore organization-only RLS policies
--
-- Reverts every policy touched by the up migration to the pre-000033
-- organization-scoped form (migration 000023 / 000021 / original table
-- migrations).

DROP POLICY IF EXISTS client_isolation ON client;
CREATE POLICY client_isolation ON client
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DO $$
DECLARE
    entry RECORD;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('site', 'site_isolation'),
            ('ci', 'ci_isolation'),
            ('collector', 'collector_isolation'),
            ('subnet', 'org_isolation'),
            ('contact', 'org_isolation'),
            ('credential', 'credential_isolation')
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
