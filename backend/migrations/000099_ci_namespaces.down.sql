DROP TRIGGER trg_ci_field_value_project ON ci_field_value;
DROP FUNCTION ci_field_value_project();

-- Instance attribute values return to the top level; the projected
-- namespaces are dropped (ci_field_value keeps their data).
UPDATE ci SET attributes = (attributes - '_instance') || COALESCE(attributes -> '_instance', '{}'::jsonb)
WHERE attributes ? '_instance';
UPDATE ci SET attributes = attributes - '_overrides' - '_observed' - '_provenance'
WHERE attributes ?| ARRAY['_overrides', '_observed', '_provenance'];

DROP FUNCTION ci_attribute_namespaces(jsonb);
DROP FUNCTION ci_field_namespaces(uuid);
