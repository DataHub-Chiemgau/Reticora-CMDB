-- Migration 000228: field rows of the full-text index (RBA-07, SRC-01,
-- WP-266).
--
-- The summary of a search document bundles typed columns of its entity,
-- whose field permissions (000226) may differ per principal. Each of them
-- is also stored as a field row with its own tsvector, so a search with a
-- restricted field policy matches the title together with the readable
-- fields only and builds summary and snippets from them. Field keys follow
-- the field permission model (core.<column>); search_document.search_vector
-- stays the index-backed prefilter and the only hit basis of contacts,
-- locations, reservations and compliance entries.
--
-- 1. search_document gets the key (id, organization_id) the field rows
--    refer to (pattern of 000136).
-- 2. search_document_field with ENABLE and FORCE ROW LEVEL SECURITY and the
--    truncate guard (000144). Its policy checks the organization in USING
--    and WITH CHECK, and USING inherits the client and site scope of the
--    document: the subquery reads search_document under its own policy
--    (000067), so a client-scoped principal reads no field contents of
--    other clients. WITH CHECK has no such subquery: the index writes a
--    document and its field rows in one statement (NFR-03), whose snapshot
--    does not show the new document yet; the foreign key binds each row
--    to a document of the same organization, which the writer upserted
--    under its own scope.
--    id names a row for the RLS matrix (TestRLSMatrix); the key is
--    (document_id, field).
-- 3. No GIN index on vector: its operator class tsvector_ops has no
--    leakproof operator, so the app role could not use it under FORCE ROW
--    LEVEL SECURITY (K22, rls.LeakyIndexes may only shrink); the search
--    prefilters with search_document.search_vector and matches the field
--    rows of its candidates by their document.
-- 4. Backfill of the field rows of existing ci, asset, document and ticket
--    documents from their entity tables, with the columns of the reindex
--    (search/pg_repository.go); an empty value gets no row.

ALTER TABLE search_document ADD CONSTRAINT search_document_id_organization_key UNIQUE (id, organization_id);

CREATE TABLE search_document_field (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL,
    document_id     UUID NOT NULL,
    field           TEXT NOT NULL,
    content         TEXT NOT NULL,
    vector          tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
    PRIMARY KEY (document_id, field),
    CONSTRAINT search_document_field_document_fkey FOREIGN KEY (document_id, organization_id)
        REFERENCES search_document (id, organization_id) ON DELETE CASCADE
);

ALTER TABLE search_document_field ENABLE ROW LEVEL SECURITY;
ALTER TABLE search_document_field FORCE ROW LEVEL SECURITY;
CREATE POLICY search_document_field_tenant_isolation ON search_document_field FOR ALL
    USING (organization_id = current_setting('app.org_id')::uuid
           AND EXISTS (SELECT 1 FROM search_document d WHERE d.id = document_id))
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON search_document_field
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

INSERT INTO search_document_field (organization_id, document_id, field, content)
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN ci e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.hostname', e.hostname), ('core.manufacturer', e.manufacturer), ('core.model', e.model),
       ('core.serial_number', e.serial_number), ('core.os_name', e.os_name), ('core.os_version', e.os_version)) AS f(field, content)
 WHERE d.entity_type = 'ci' AND f.content <> ''
UNION ALL
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN asset e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.asset_tag', e.asset_tag), ('core.category', e.category), ('core.status', e.status),
       ('core.supplier', e.supplier), ('core.serial_number', e.serial_number), ('core.location', e.location),
       ('core.notes', e.notes)) AS f(field, content)
 WHERE d.entity_type = 'asset' AND f.content <> ''
UNION ALL
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN document e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.description', e.description), ('core.file_name', e.file_name), ('core.category', e.category),
       ('core.tags', array_to_string(e.tags, ' '))) AS f(field, content)
 WHERE d.entity_type = 'document' AND f.content <> ''
UNION ALL
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN ticket e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.description', e.description), ('core.status', e.status), ('core.priority', e.priority),
       ('core.category', e.category), ('core.tags', array_to_string(e.tags, ' '))) AS f(field, content)
 WHERE d.entity_type = 'ticket' AND f.content <> '';
