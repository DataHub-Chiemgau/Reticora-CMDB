-- Down of 000232: the attribute schema of the relationship types goes; the
-- attributes of the edges stay.

ALTER TABLE relationship_type DROP COLUMN metadata_schema;
