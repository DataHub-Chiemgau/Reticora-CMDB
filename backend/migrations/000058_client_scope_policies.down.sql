-- Revert migration 000058: restore the policies of migrations 000033 (client
-- scope with writable NULL client) and the organization-only policies of the
-- nine tables without client scope.

DO $$
DECLARE
    entry RECORD;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('asset', 'asset_tenant_isolation'),
            ('consumable', 'consumable_isolation'),
            ('form_def', 'form_def_tenant_isolation'),
            ('internal_order', 'internal_order_isolation'),
            ('key_item', 'key_item_isolation'),
            ('location_node', 'location_node_isolation'),
            ('maintenance_notification', 'maintenance_notification_isolation'),
            ('quantity_item', 'quantity_item_isolation'),
            ('sla', 'sla_tenant_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (organization_id = current_setting(''app.org_id'')::UUID) '
            'WITH CHECK (organization_id = current_setting(''app.org_id'')::UUID)',
            entry.policy_name, entry.table_name
        );
    END LOOP;

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
            'CREATE POLICY %I ON %I USING ('
            'organization_id = current_setting(''app.org_id'')::UUID '
            'AND (NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
            'OR client_id IS NULL '
            'OR client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]))'
            ') WITH CHECK ('
            'organization_id = current_setting(''app.org_id'')::UUID '
            'AND (NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
            'OR client_id IS NULL '
            'OR client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]))'
            ')',
            entry.policy_name, entry.table_name
        );
    END LOOP;
END
$$;
