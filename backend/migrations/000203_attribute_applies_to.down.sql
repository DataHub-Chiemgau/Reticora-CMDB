-- Down of 000203: the applicability columns and their CHECKs go away; every
-- organization global applies to every type again.

ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_applies_to_list_check;
ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_applies_to_scope_check;
ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_applies_to_check;

ALTER TABLE attribute_definition
    DROP COLUMN applies_to_type_ids,
    DROP COLUMN applies_to_categories,
    DROP COLUMN applies_to;
