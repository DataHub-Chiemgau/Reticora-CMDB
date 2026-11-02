-- WP-353 (TEN-05, TEN-09; F-354, E-09): a principal restricted in the client
-- or site scope changes or deletes a document or maintenance window only when
-- it may write every one of its links.
--
-- Migration 000139 compares the counter with the links visible under RLS.
-- A link to an org-wide object (client_id or site_id NULL) is visible to
-- every principal, so a client-1 principal renamed and deleted a document or
-- window linked only to an org-wide CI, or to its own and an org-wide CI; the
-- foreign key cascade removed the org-wide link, which it may neither create
-- nor delete itself (E-09). The RLS matrix of WP-353 found it.
--
-- The restrictive UPDATE and DELETE policies now count only the links whose
-- client and site are in the principal's scope (the WITH CHECK expression of
-- the link tables, 000064). Reading, inserting and the org-wide principals
-- are unchanged.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    org_wide CONSTANT text := '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
                              'AND NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL)';
    link_writable CONSTANT text := '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
                                   'OR l.client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])) '
                                   'AND (NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL '
                                   'OR l.site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]))';
    writable text;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('document', 'link_count', 'document_link', 'document_id'),
            ('maintenance_window', 'ci_count', 'maintenance_window_ci', 'maintenance_window_id')
        ) AS t(table_name, counter, link_table, link_column)
    LOOP
        writable := format('%1$s AND (%2$s OR (%3$I > 0 AND %3$I = (SELECT count(*) FROM %4$I l WHERE l.%5$I = %6$I.id AND %7$s)))',
                           org_match, org_wide, entry.counter, entry.link_table, entry.link_column, entry.table_name, link_writable);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_update', entry.table_name);
        EXECUTE format('DROP POLICY %I ON %I', entry.table_name || '_scoped_delete', entry.table_name);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       entry.table_name || '_scoped_update', entry.table_name, writable, writable);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s)',
                       entry.table_name || '_scoped_delete', entry.table_name, writable);
    END LOOP;
END
$$;
