-- Down of 000228: the field rows with their index, trigger and policy, then
-- the key of search_document they refer to.

DROP TABLE search_document_field;
ALTER TABLE search_document DROP CONSTRAINT search_document_id_organization_key;
