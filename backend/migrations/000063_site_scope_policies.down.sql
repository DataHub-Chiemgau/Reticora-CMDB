-- Reverts WP-027: the policies of migrations 000058/000061 without the site
-- predicate.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
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
            ('subnet', 'org_isolation'),
            ('site', 'site_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %1$I ON %2$I '
            'USING (%3$s AND (%4$s OR client_id IS NULL OR client_id = ANY (%5$s))) '
            'WITH CHECK (%3$s AND (%4$s OR client_id = ANY (%5$s)))',
            entry.policy_name, entry.table_name, org_match, client_all, client_ids);
    END LOOP;

    DROP POLICY building_isolation ON building;
    EXECUTE format('CREATE POLICY building_isolation ON building USING (%1$s) WITH CHECK (%1$s)', org_match);

    FOR entry IN
        SELECT * FROM (VALUES
            ('ci_relationship', 'ci_relationship_isolation'),
            ('relationship_suppression', 'suppression_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %1$I ON %2$I '
            'USING (%3$s AND (%4$s OR ((source_client_id IS NULL OR source_client_id = ANY (%5$s)) '
                'AND (target_client_id IS NULL OR target_client_id = ANY (%5$s))))) '
            'WITH CHECK (%3$s AND (%4$s OR (source_client_id = ANY (%5$s) AND target_client_id = ANY (%5$s))))',
            entry.policy_name, entry.table_name, org_match, client_all, client_ids);
    END LOOP;
END
$$;
