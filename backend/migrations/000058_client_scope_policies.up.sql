-- Migration 000058: client scope for all tables with client_id (WP-023)
--
-- TEN-05 / CH11: nine tables with a client_id column only checked the
-- organization (asset, consumable, form_def, internal_order, key_item,
-- location_node, maintenance_notification, quantity_item, sla). They now use
-- app.client_scope like the tables of migration 000033.
--
-- E-09: only principals with an org-wide client scope (app.client_scope empty)
-- may write rows with client_id IS NULL. For all fifteen tables with a
-- client_id column WITH CHECK therefore no longer accepts client_id IS NULL
-- for a restricted scope; USING keeps such shared rows readable.
--
-- An empty or unset app.client_scope means all clients of the organization
-- (TEN-04, NULL = all); database.WithTenant encodes "no client" as the nil
-- UUID, which matches no row.

DO $$
DECLARE
    entry RECORD;
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
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
            ('sla', 'sla_tenant_isolation'),
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
            'CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s)) '
            'WITH CHECK (%s AND (%s OR %s))',
            entry.policy_name, entry.table_name,
            org_match, scope_all, scope_match,
            org_match, scope_all, scope_match
        );
    END LOOP;
END
$$;
