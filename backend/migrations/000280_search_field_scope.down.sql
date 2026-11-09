-- Down of 000280: the policy of 000228 with the lookup of the document,
-- without the scope columns and their triggers.

DROP POLICY search_document_field_scoped_delete ON search_document_field;
DROP POLICY search_document_field_scoped_update ON search_document_field;
DROP POLICY search_document_field_tenant_isolation ON search_document_field;
CREATE POLICY search_document_field_tenant_isolation ON search_document_field FOR ALL
    USING (organization_id = current_setting('app.org_id')::uuid
           AND EXISTS (SELECT 1 FROM search_document d WHERE d.id = document_id))
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);

DROP TRIGGER search_field_scope_propagation ON search_document;
DROP FUNCTION propagate_search_field_scope();
DROP TRIGGER search_field_scope ON search_document_field;
DROP FUNCTION search_field_scope();

DROP INDEX IF EXISTS idx_search_document_field_match;
ALTER TABLE search_document_field DROP COLUMN client_id, DROP COLUMN site_id;
