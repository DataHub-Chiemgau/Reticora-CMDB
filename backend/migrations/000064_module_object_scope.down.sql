-- Reverts WP-028: organization-only policies, no derived scope columns.

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('ticket', 'ticket_tenant_isolation'),
            ('ticket_comment', 'ticket_comment_tenant_isolation'),
            ('document_link', 'document_link_tenant_isolation'),
            ('maintenance_window_ci', 'maintenance_window_ci_isolation'),
            ('key_assignment', 'key_assignment_isolation'),
            ('document', 'document_tenant_isolation'),
            ('maintenance_window', 'maintenance_window_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format('CREATE POLICY %1$I ON %2$I USING (%3$s) WITH CHECK (%3$s)',
            entry.policy_name, entry.table_name, org_match);
    END LOOP;
END
$$;

DROP TRIGGER IF EXISTS document_link_parent_visible ON document_link;
DROP TRIGGER IF EXISTS maintenance_window_ci_parent_visible ON maintenance_window_ci;
DROP TRIGGER IF EXISTS document_link_count ON document_link;
DROP TRIGGER IF EXISTS maintenance_window_ci_count ON maintenance_window_ci;
DROP TRIGGER IF EXISTS ci_module_scope_propagation ON ci;
DROP TRIGGER IF EXISTS asset_module_scope_propagation ON asset;
DROP TRIGGER IF EXISTS ticket_scope_propagation ON ticket;
DROP TRIGGER IF EXISTS key_item_scope_propagation ON key_item;
DROP TRIGGER IF EXISTS ticket_scope ON ticket;
DROP TRIGGER IF EXISTS ticket_comment_scope ON ticket_comment;
DROP TRIGGER IF EXISTS document_link_scope ON document_link;
DROP TRIGGER IF EXISTS maintenance_window_ci_scope ON maintenance_window_ci;
DROP TRIGGER IF EXISTS key_assignment_scope ON key_assignment;

DROP FUNCTION IF EXISTS require_visible_parent();
DROP FUNCTION IF EXISTS count_document_links();
DROP FUNCTION IF EXISTS count_maintenance_window_cis();
DROP FUNCTION IF EXISTS propagate_ci_module_scope();
DROP FUNCTION IF EXISTS propagate_asset_module_scope();
DROP FUNCTION IF EXISTS propagate_ticket_scope();
DROP FUNCTION IF EXISTS propagate_key_item_scope();
DROP FUNCTION IF EXISTS derive_ticket_scope();
DROP FUNCTION IF EXISTS derive_ticket_comment_scope();
DROP FUNCTION IF EXISTS derive_document_link_scope();
DROP FUNCTION IF EXISTS derive_key_assignment_scope();

ALTER TABLE ticket DROP COLUMN IF EXISTS client_id, DROP COLUMN IF EXISTS site_id;
ALTER TABLE ticket_comment DROP COLUMN IF EXISTS client_id, DROP COLUMN IF EXISTS site_id;
ALTER TABLE document_link DROP COLUMN IF EXISTS client_id, DROP COLUMN IF EXISTS site_id;
ALTER TABLE maintenance_window_ci DROP COLUMN IF EXISTS client_id, DROP COLUMN IF EXISTS site_id;
ALTER TABLE key_assignment DROP COLUMN IF EXISTS client_id;
ALTER TABLE document DROP COLUMN IF EXISTS link_count;
ALTER TABLE maintenance_window DROP COLUMN IF EXISTS ci_count;
