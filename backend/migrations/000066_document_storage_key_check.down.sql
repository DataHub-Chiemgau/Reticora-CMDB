-- Reverts WP-030: drops the prefix check. Keys cleared by the up migration
-- stay empty.

ALTER TABLE document DROP CONSTRAINT IF EXISTS document_storage_key_org_prefix;
