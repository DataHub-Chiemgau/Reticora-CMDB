-- WP-255 (MET-13): applicability of the organization's global attributes.
--
-- applies_to restricts an organization-wide definition (scope 'global',
-- organization_id NOT NULL) to CI types of given categories
-- (ci_type.category, free text) or to given types and their subtypes (type
-- chain, MET-06). System globals and type or set definitions stay 'all'.
-- Existing rows get 'all' and keep their behavior. The type IDs are checked
-- by the server when written (no foreign key on array elements); IDs of
-- types that no longer exist are ignored.
--
-- The materialized schema (generate_attribute_schema,
-- attribute_definition_regenerate, ci_type_schema_guard) is unchanged: the
-- organization's globals stay a runtime layer (E-93, E-108), so a change of
-- applies_to raises no schema_version. Indexes and uniqueness stay
-- organization-wide (no type predicate). No new table; row level security
-- and the policies of attribute_definition are unchanged.

ALTER TABLE attribute_definition
    ADD COLUMN applies_to TEXT NOT NULL DEFAULT 'all',
    ADD COLUMN applies_to_categories TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN applies_to_type_ids UUID[] NOT NULL DEFAULT '{}';

ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_applies_to_check
    CHECK (applies_to IN ('all', 'categories', 'types'));

-- Only the organization's global attributes can be restricted.
ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_applies_to_scope_check
    CHECK (applies_to = 'all' OR (scope = 'global' AND organization_id IS NOT NULL));

-- The list of the mode is not empty, the other one is.
ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_applies_to_list_check
    CHECK ((applies_to = 'all' AND cardinality(applies_to_categories) = 0 AND cardinality(applies_to_type_ids) = 0)
        OR (applies_to = 'categories' AND cardinality(applies_to_categories) > 0 AND cardinality(applies_to_type_ids) = 0)
        OR (applies_to = 'types' AND cardinality(applies_to_type_ids) > 0 AND cardinality(applies_to_categories) = 0));
