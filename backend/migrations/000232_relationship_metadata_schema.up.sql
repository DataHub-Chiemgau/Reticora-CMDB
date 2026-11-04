-- Migration 000232: attribute schema of a relationship type (REL-07,
-- WP-295). metadata_schema is the list of definitions that describes
-- ci_relationship.attributes (the metadata of REL-02, migration 000114) in
-- the shape of attribute_definition (key, display_name, data_type,
-- required, default, enum_values, validation, conditional). The API checks
-- the definitions (relationshiptype.Type.Validate) and the attributes of
-- POST and PATCH /relationships against them. System rows and the existing
-- organization types get [] through the default; RLS stays as it is
-- (policies relationship_type_isolation_* of 000059).

ALTER TABLE relationship_type
    ADD COLUMN metadata_schema jsonb NOT NULL DEFAULT '[]'::jsonb
    CONSTRAINT relationship_type_metadata_schema_array CHECK (jsonb_typeof(metadata_schema) = 'array');
