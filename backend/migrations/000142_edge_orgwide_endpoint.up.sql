-- WP-345 follow-up (TEN-05, IMP-07; F-182, decision E-62 option 2): a client-
-- or site-scoped principal may write an edge between its own CI and a
-- visible org-wide CI.
--
-- Until now the write policies of ci_relationship and relationship_suppression
-- (000063, restrictive UPDATE/DELETE copies of 000079) required both endpoint
-- clients and both endpoint sites in the scope, so an endpoint without client
-- or site (NULL, org-wide) refused the edge. Per dimension (client, site) the
-- write predicate now is
--
--   (source NULL or in scope) AND (target NULL or in scope)
--   AND (source in scope OR target in scope)
--
-- so at least one endpoint carries a client (site) of the scope. Such an edge
-- is not org-wide: its derived source_/target_ columns hold the client and
-- site of the own endpoint, and the unchanged USING shows it only to
-- principals whose scope contains them (and to org-wide principals). Edges
-- between two org-wide endpoints stay reserved to org-wide principals (E-09);
-- an endpoint of a foreign client or site is refused as before. The
-- propagation triggers (000141) re-derive with the scope widened and pass the
-- policies unchanged. network_interface has a single CI and keeps its policy:
-- an interface on an org-wide CI is an org-wide row (E-09).
--
-- derive_edge_scope refuses an endpoint that is not a CI of the row's
-- organization. relationship_suppression has no tenant foreign key, and such
-- an endpoint was derived as NULL; with the relaxed predicate it would now
-- pass as an org-wide endpoint.

CREATE OR REPLACE FUNCTION derive_edge_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    SELECT c.client_id, c.site_id INTO NEW.source_client_id, NEW.source_site_id
      FROM ci c WHERE c.id = NEW.source_ci_id AND c.organization_id = NEW.organization_id;
    IF NOT FOUND THEN
        PERFORM app_scope_restore(previous);
        RAISE EXCEPTION 'source CI is not a CI of the organization'
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    SELECT c.client_id, c.site_id INTO NEW.target_client_id, NEW.target_site_id
      FROM ci c WHERE c.id = NEW.target_ci_id AND c.organization_id = NEW.organization_id;
    IF NOT FOUND THEN
        PERFORM app_scope_restore(previous);
        RAISE EXCEPTION 'target CI is not a CI of the organization'
            USING ERRCODE = 'foreign_key_violation';
    END IF;
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
        'AND (%2$s OR ((source_client_id IS NULL OR source_client_id = ANY (%3$s)) '
            'AND (target_client_id IS NULL OR target_client_id = ANY (%3$s)) '
            'AND (source_client_id = ANY (%3$s) OR target_client_id = ANY (%3$s)))) '
        'AND (%4$s OR ((source_site_id IS NULL OR source_site_id = ANY (%5$s)) '
            'AND (target_site_id IS NULL OR target_site_id = ANY (%5$s)) '
            'AND (source_site_id = ANY (%5$s) OR target_site_id = ANY (%5$s))))',
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
