-- Reverts WP-584: relationship_suppression without created_by and reason.

ALTER TABLE relationship_suppression
    DROP CONSTRAINT relationship_suppression_reason_check,
    DROP CONSTRAINT relationship_suppression_created_by_fkey,
    DROP COLUMN reason,
    DROP COLUMN created_by;
