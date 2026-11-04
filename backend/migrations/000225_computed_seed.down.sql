-- Removes the system definition warranty_days_left of 000225 with its
-- values: the key in ci.attributes of the CIs with a computed value of it,
-- their computed provenance rows and the definition
-- (trg_attribute_definition_regenerate regenerates every type schema
-- without it).

UPDATE ci SET attributes = attributes - 'warranty_days_left'
WHERE id IN (SELECT v.ci_id FROM ci_field_value v
             WHERE v.field_name = 'warranty_days_left' AND v.discovered_source = 'computed');

DELETE FROM ci_field_value WHERE field_name = 'warranty_days_left' AND discovered_source = 'computed';

DELETE FROM attribute_definition WHERE id = 'a7180000-0000-4000-8000-000000000001';
