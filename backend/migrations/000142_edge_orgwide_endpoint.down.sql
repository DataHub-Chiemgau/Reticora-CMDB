-- Reverts the E-62 option 2 follow-up of WP-345: both endpoint clients and
-- sites of an edge written by a scoped principal must be in the scope again
-- (000063, 000079), derive_edge_scope as in 000141.

CREATE OR REPLACE FUNCTION derive_edge_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    SELECT c.client_id, c.site_id INTO NEW.source_client_id, NEW.source_site_id
      FROM ci c WHERE c.id = NEW.source_ci_id AND c.organization_id = NEW.organization_id;
    SELECT c.client_id, c.site_id INTO NEW.target_client_id, NEW.target_site_id
      FROM ci c WHERE c.id = NEW.target_ci_id AND c.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    write_check text;
BEGIN
    write_check := format(
        '%1$s '
        'AND (%2$s OR (source_client_id = ANY (%3$s) AND target_client_id = ANY (%3$s))) '
        'AND (%4$s OR (source_site_id = ANY (%5$s) AND target_site_id = ANY (%5$s)))',
        org_match, client_all, client_ids, site_all, site_ids);
    FOR entry IN
        SELECT * FROM (VALUES
            ('ci_relationship', 'ci_relationship_isolation'),
            ('relationship_suppression', 'suppression_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('ALTER POLICY %I ON %I WITH CHECK (%s)', entry.policy_name, entry.table_name, write_check);
        EXECUTE format('ALTER POLICY %I ON %I USING (%s) WITH CHECK (%s)',
                       entry.table_name || '_scoped_update', entry.table_name, write_check, write_check);
        EXECUTE format('ALTER POLICY %I ON %I USING (%s)',
                       entry.table_name || '_scoped_delete', entry.table_name, write_check);
    END LOOP;
END
$$;
