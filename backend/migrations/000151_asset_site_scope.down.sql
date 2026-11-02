-- Reverts WP-392: assets and quantity items without site; client-only
-- policies of migrations 000058 and 000079.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    writable text := format('%s AND (%s OR %s)', org_match, scope_all, scope_match);
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('asset', 'asset_tenant_isolation'),
            ('quantity_item', 'quantity_item_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_delete', entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_update', entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s)) WITH CHECK (%s)',
            entry.policy_name, entry.table_name, org_match, scope_all, scope_match, writable);
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       entry.table_name || '_scoped_update', entry.table_name, writable, writable);
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR DELETE USING (%s)',
                       entry.table_name || '_scoped_delete', entry.table_name, writable);
    END LOOP;
END
$$;

DROP TRIGGER trg_location_refresh_asset_site ON location;
DROP FUNCTION location_refresh_asset_site();
DROP TRIGGER trg_quantity_item_derive_site ON quantity_item;
DROP TRIGGER trg_asset_derive_site ON asset;
DROP FUNCTION derive_location_site();

DROP INDEX idx_quantity_item_site;
DROP INDEX idx_asset_site;
ALTER TABLE quantity_item DROP COLUMN site_id;
ALTER TABLE asset DROP COLUMN site_id;
