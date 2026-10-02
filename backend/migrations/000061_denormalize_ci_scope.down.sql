-- Migration 000061 down: back to organization-only policies, drop the derived
-- client and site columns of the CI child tables.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('network_interface', 'org_isolation'),
            ('ip_address', 'org_isolation'),
            ('ci_contact', 'org_isolation'),
            ('ci_change', 'org_isolation'),
            ('ci_field_value', 'ci_field_value_isolation'),
            ('ci_instance_field_definition', 'ci_instance_field_definition_isolation'),
            ('rack_mount', 'rack_mount_isolation'),
            ('compliance_result', 'compliance_result_tenant_isolation'),
            ('security_finding', 'security_finding_isolation'),
            ('discovery_result', 'discovery_result_isolation'),
            ('ci_relationship', 'ci_relationship_isolation'),
            ('relationship_suppression', 'suppression_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format('CREATE POLICY %I ON %I USING (%s) WITH CHECK (%s)',
            entry.policy_name, entry.table_name, org_match, org_match);
    END LOOP;
END
$$;

DROP TRIGGER IF EXISTS ci_scope_propagation ON ci;
DROP TRIGGER IF EXISTS network_interface_scope_propagation ON network_interface;
DROP TRIGGER IF EXISTS subnet_scope_propagation ON subnet;
DROP FUNCTION IF EXISTS propagate_ci_scope();
DROP FUNCTION IF EXISTS propagate_interface_scope();
DROP FUNCTION IF EXISTS propagate_subnet_scope();

DROP TRIGGER IF EXISTS network_interface_scope ON network_interface;
DROP TRIGGER IF EXISTS ci_contact_scope ON ci_contact;
DROP TRIGGER IF EXISTS ci_change_scope ON ci_change;
DROP TRIGGER IF EXISTS ci_field_value_scope ON ci_field_value;
DROP TRIGGER IF EXISTS ci_instance_field_definition_scope ON ci_instance_field_definition;
DROP TRIGGER IF EXISTS rack_mount_scope ON rack_mount;
DROP TRIGGER IF EXISTS compliance_result_scope ON compliance_result;
DROP TRIGGER IF EXISTS security_finding_scope ON security_finding;
DROP TRIGGER IF EXISTS discovery_result_scope ON discovery_result;
DROP TRIGGER IF EXISTS ip_address_scope ON ip_address;
DROP TRIGGER IF EXISTS ci_relationship_scope ON ci_relationship;
DROP TRIGGER IF EXISTS relationship_suppression_scope ON relationship_suppression;
DROP FUNCTION IF EXISTS derive_ci_scope();
DROP FUNCTION IF EXISTS derive_edge_scope();
DROP FUNCTION IF EXISTS derive_ip_scope();
DROP FUNCTION IF EXISTS derive_discovery_result_scope();

DROP INDEX IF EXISTS idx_network_interface_client;
DROP INDEX IF EXISTS idx_ip_address_client;
DROP INDEX IF EXISTS idx_ci_relationship_clients;

ALTER TABLE network_interface DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE ip_address DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE ci_contact DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE ci_change DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE ci_field_value DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE ci_instance_field_definition DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE rack_mount DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE compliance_result DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE security_finding DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE discovery_result DROP COLUMN client_id, DROP COLUMN site_id;
ALTER TABLE ci_relationship
    DROP COLUMN source_client_id, DROP COLUMN source_site_id,
    DROP COLUMN target_client_id, DROP COLUMN target_site_id;
ALTER TABLE relationship_suppression
    DROP COLUMN source_client_id, DROP COLUMN source_site_id,
    DROP COLUMN target_client_id, DROP COLUMN target_site_id;
