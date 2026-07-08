ALTER TABLE IF EXISTS document_link DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS document DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS document_link_tenant_isolation ON document_link;
DROP POLICY IF EXISTS document_tenant_isolation ON document;

DROP INDEX IF EXISTS idx_doc_link_entity;
DROP INDEX IF EXISTS idx_doc_link_doc;
DROP INDEX IF EXISTS idx_document_category;
DROP INDEX IF EXISTS idx_document_org;

DROP TABLE IF EXISTS document_link;
DROP TABLE IF EXISTS document;
