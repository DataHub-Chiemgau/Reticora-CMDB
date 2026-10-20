-- Reverts WP-353: the restrictive UPDATE and DELETE policies of migration
-- 000139 count every link visible under RLS.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    org_wide CONSTANT text := '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
                              'AND NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL)';
    writable text;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('document', 'link_count', 'document_link', 'document_id'),
            ('maintenance_window', 'ci_count', 'maintenance_window_ci', 'maintenance_window_id')
        ) AS t(table_name, counter, link_table, link_column)
    LOOP
        writable := format('%1$s AND (%2$s OR (%3$I > 0 AND %3$I = (SELECT count(*) FROM %4$I l WHERE l.%5$I = %6$I.id)))',
                           org_match, org_wide, entry.counter, entry.link_table, entry.link_column, entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_update', entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_delete', entry.table_name);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       entry.table_name || '_scoped_update', entry.table_name, writable, writable);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s)',
                       entry.table_name || '_scoped_delete', entry.table_name, writable);
    END LOOP;
END
$$;
