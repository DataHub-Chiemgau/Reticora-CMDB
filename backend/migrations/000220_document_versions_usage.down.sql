-- Down of 000220: the version and usage tables with their triggers, policies
-- and the key check function. The mirror columns of document (version,
-- storage_key, file_size, mime_type) keep the current version.

DROP TABLE organization_usage;
DROP TABLE document_version;
DROP FUNCTION document_version_storage_key();
ALTER TABLE document DROP CONSTRAINT document_id_organization_key;
