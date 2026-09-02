DELETE FROM search_document WHERE entity_type IN ('location','reservation');
ALTER TABLE search_document DROP CONSTRAINT IF EXISTS search_document_entity_type_check;
ALTER TABLE search_document ADD CONSTRAINT search_document_entity_type_check
    CHECK (entity_type IN ('ci','asset','document','ticket','contact','compliance'));
