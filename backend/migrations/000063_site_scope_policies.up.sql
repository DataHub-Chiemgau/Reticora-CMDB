-- WP-027 (CH25, TEN-04, TEN-05): site predicate in every policy of a table
-- with an own or denormalized site_id.
--
-- The predicate reads app.site_scope like the client predicate reads
-- app.client_scope: an empty GUC grants every site of the organization. NULL
-- semantics follow E-09: USING keeps rows without site (site_id IS NULL)
-- readable, WITH CHECK lets only org-wide principals write them.
--
-- - CI and its child tables, subnet and location_node: client predicate of
--   migrations 000058/000061 plus the site predicate on site_id.
-- - building: organization plus site predicate (no client column).
-- - site: the row is its own site, so the predicate applies to id.
-- - ci_relationship and relationship_suppression: both endpoints
--   (source_site_id, target_site_id) must be in scope.
-- location (migration 000062) carries the site predicate already.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('ci', 'ci_isolation'),
            ('ci_change', 'org_isolation'),
            ('ci_contact', 'org_isolation'),
            ('ci_field_value', 'ci_field_value_isolation'),
            ('ci_instance_field_definition', 'ci_instance_field_definition_isolation'),
            ('compliance_result', 'compliance_result_tenant_isolation'),
            ('discovery_result', 'discovery_result_isolation'),
            ('ip_address', 'org_isolation'),
            ('location_node', 'location_node_isolation'),
            ('network_interface', 'org_isolation'),
            ('rack_mount', 'rack_mount_isolation'),
            ('security_finding', 'security_finding_isolation'),
            ('subnet', 'org_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %1$I ON %2$I '
            'USING (%3$s AND (%4$s OR client_id IS NULL OR client_id = ANY (%5$s)) '
                'AND (%6$s OR site_id IS NULL OR site_id = ANY (%7$s))) '
            'WITH CHECK (%3$s AND (%4$s OR client_id = ANY (%5$s)) '
                'AND (%6$s OR site_id = ANY (%7$s)))',
            entry.policy_name, entry.table_name,
            org_match, client_all, client_ids, site_all, site_ids);
    END LOOP;

    DROP POLICY building_isolation ON building;
    EXECUTE format(
        'CREATE POLICY building_isolation ON building '
        'USING (%1$s AND (%2$s OR site_id = ANY (%3$s))) '
        'WITH CHECK (%1$s AND (%2$s OR site_id = ANY (%3$s)))',
        org_match, site_all, site_ids);

    DROP POLICY site_isolation ON site;
    EXECUTE format(
        'CREATE POLICY site_isolation ON site '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR id = ANY (%5$s))) '
        'WITH CHECK (%1$s AND (%2$s OR client_id = ANY (%3$s)) '
            'AND (%4$s OR id = ANY (%5$s)))',
        org_match, client_all, client_ids, site_all, site_ids);

    FOR entry IN
        SELECT * FROM (VALUES
            ('ci_relationship', 'ci_relationship_isolation'),
            ('relationship_suppression', 'suppression_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %1$I ON %2$I '
            'USING (%3$s '
                'AND (%4$s OR ((source_client_id IS NULL OR source_client_id = ANY (%5$s)) '
                    'AND (target_client_id IS NULL OR target_client_id = ANY (%5$s)))) '
                'AND (%6$s OR ((source_site_id IS NULL OR source_site_id = ANY (%7$s)) '
                    'AND (target_site_id IS NULL OR target_site_id = ANY (%7$s))))) '
            'WITH CHECK (%3$s '
                'AND (%4$s OR (source_client_id = ANY (%5$s) AND target_client_id = ANY (%5$s))) '
                'AND (%6$s OR (source_site_id = ANY (%7$s) AND target_site_id = ANY (%7$s))))',
            entry.policy_name, entry.table_name,
            org_match, client_all, client_ids, site_all, site_ids);
    END LOOP;
END
$$;
