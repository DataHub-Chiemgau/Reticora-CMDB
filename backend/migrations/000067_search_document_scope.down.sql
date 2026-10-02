-- Reverts WP-032: organization-only policy, no derived scope columns, the
-- previous entity type check. Rows of the types it does not admit are
-- removed; a reindex recreates them once the check admits them again.

DROP POLICY search_document_tenant_isolation ON search_document;
CREATE POLICY search_document_tenant_isolation ON search_document
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER IF EXISTS ci_search_scope_propagation ON ci;
DROP TRIGGER IF EXISTS ticket_search_scope_propagation ON ticket;
DROP TRIGGER IF EXISTS location_node_search_scope_propagation ON location_node;
DROP TRIGGER IF EXISTS asset_search_scope_propagation ON asset;
DROP TRIGGER IF EXISTS contact_search_scope_propagation ON contact;
DROP TRIGGER IF EXISTS reservation_search_scope_propagation ON reservation;
DROP TRIGGER IF EXISTS search_document_scope ON search_document;
DROP FUNCTION IF EXISTS propagate_search_scope();
DROP FUNCTION IF EXISTS derive_search_document_scope();

DROP INDEX IF EXISTS idx_search_document_client;
ALTER TABLE search_document DROP COLUMN IF EXISTS client_id, DROP COLUMN IF EXISTS site_id;

DELETE FROM search_document WHERE entity_type IN ('location', 'reservation');
ALTER TABLE search_document DROP CONSTRAINT search_document_entity_type_check;
ALTER TABLE search_document ADD CONSTRAINT search_document_entity_type_check
    CHECK (entity_type = ANY (ARRAY['ci'::text, 'asset'::text, 'document'::text, 'ticket'::text, 'contact'::text, 'compliance'::text]));
