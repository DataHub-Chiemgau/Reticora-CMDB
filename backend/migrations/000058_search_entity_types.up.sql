-- Allow search indexing of location and reservation entities. The reindex
-- pipeline (search.PGRepository.ReindexTenant) inserts these entity types;
-- the original CHECK constraint rejected them, rolling back every reindex.
ALTER TABLE search_document DROP CONSTRAINT IF EXISTS search_document_entity_type_check;
ALTER TABLE search_document ADD CONSTRAINT search_document_entity_type_check
    CHECK (entity_type IN ('ci','asset','document','ticket','contact','compliance','location','reservation'));
