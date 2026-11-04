-- Down of 000222: drops the index of the open scans and scan_signature; the
-- scan status of 000220 stays.

DROP INDEX IF EXISTS idx_document_version_scan_open;
ALTER TABLE document_version DROP COLUMN scan_signature;
