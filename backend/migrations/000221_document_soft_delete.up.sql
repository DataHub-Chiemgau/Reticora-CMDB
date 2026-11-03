-- WP-732 (MGT-01): soft delete of documents. DELETE /api/v1/documents/{id}
-- sets deleted_at; the retention run retention.soft_deleted removes the
-- document after RETICORA_RETENTION_SOFT_DELETE_DAYS (at least 90 days)
-- with its versions and links, sets delete markers on its blobs and lowers
-- organization_usage. The policies do not change: the repository and the
-- search hide deleted documents, so the retention run sees them under its
-- org-wide scope.

ALTER TABLE document ADD COLUMN deleted_at timestamptz;

-- The column is new and empty, so the index builds without a long lock.
CREATE INDEX IF NOT EXISTS idx_document_deleted ON document (organization_id, deleted_at) WHERE deleted_at IS NOT NULL;
