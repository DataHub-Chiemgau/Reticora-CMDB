-- Down of 000221: soft-deleted documents become visible again.

DROP INDEX IF EXISTS idx_document_deleted;
ALTER TABLE document DROP COLUMN deleted_at;
