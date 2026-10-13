-- Migration 000107 down: the generator renders the type's definitions
-- alone again; the CI loses its schema version.

DROP TRIGGER trg_ci_schema_version ON ci;
DROP FUNCTION ci_schema_version();
ALTER TABLE ci DROP COLUMN schema_version;

CREATE OR REPLACE FUNCTION ci_type_schema_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('reticora.schema_generation', true) IS NOT DISTINCT FROM 'on' THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.attribute_schema := generate_attribute_schema(NULL, COALESCE(NEW.key, NEW.name));
        NEW.required_fields := '{}';
        NEW.schema_version := 1;
    ELSIF NEW.attribute_schema IS DISTINCT FROM OLD.attribute_schema
       OR NEW.required_fields IS DISTINCT FROM OLD.required_fields
       OR NEW.schema_version IS DISTINCT FROM OLD.schema_version THEN
        RAISE EXCEPTION 'attribute_schema, required_fields and schema_version are generated from attribute_definition (DB-03)'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION attribute_definition_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM regenerate_attribute_schema(OLD.ci_type_id);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM regenerate_attribute_schema(NEW.ci_type_id);
    END IF;
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION generate_attribute_schema(p_type uuid, p_key text) RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object(
        '$schema', 'https://json-schema.org/draft/2020-12/schema',
        '$id', 'urn:reticora:ci-type:' || p_key,
        'type', 'object',
        'properties', COALESCE(jsonb_object_agg(d.key, attribute_json_schema(d)) FILTER (WHERE d.id IS NOT NULL), '{}'::jsonb),
        'required', COALESCE(jsonb_agg(d.key ORDER BY d.key) FILTER (WHERE d.required), '[]'::jsonb))
    FROM (SELECT 1) one
    LEFT JOIN attribute_definition d ON d.ci_type_id = p_type
$$;

CREATE OR REPLACE FUNCTION attribute_json_schema(d attribute_definition) RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    p jsonb;
    v jsonb := COALESCE(d.validation, '{}'::jsonb);
BEGIN
    p := CASE d.data_type
        WHEN 'integer' THEN '{"type": "integer"}'::jsonb
        WHEN 'decimal' THEN '{"type": "number"}'::jsonb
        WHEN 'number' THEN '{"type": "number"}'::jsonb
        WHEN 'boolean' THEN '{"type": "boolean"}'::jsonb
        WHEN 'date' THEN '{"type": "string", "format": "date"}'::jsonb
        WHEN 'datetime' THEN '{"type": "string", "format": "date-time"}'::jsonb
        WHEN 'enum' THEN jsonb_build_object('type', 'string', 'enum', COALESCE(d.enum_values, '[]'::jsonb))
        WHEN 'multi_enum' THEN jsonb_build_object('type', 'array', 'uniqueItems', true,
            'items', jsonb_build_object('type', 'string', 'enum', COALESCE(d.enum_values, '[]'::jsonb)))
        WHEN 'multiselect' THEN jsonb_build_object('type', 'array', 'uniqueItems', true,
            'items', jsonb_build_object('type', 'string', 'enum', COALESCE(d.enum_values, '[]'::jsonb)))
        WHEN 'ip' THEN '{"type": "string", "anyOf": [{"format": "ipv4"}, {"format": "ipv6"}]}'::jsonb
        WHEN 'mac' THEN '{"type": "string", "pattern": "^([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}$"}'::jsonb
        WHEN 'url' THEN '{"type": "string", "format": "uri"}'::jsonb
        WHEN 'email' THEN '{"type": "string", "format": "email"}'::jsonb
        WHEN 'json' THEN '{}'::jsonb
        WHEN 'uuid' THEN '{"type": "string", "format": "uuid"}'::jsonb
        WHEN 'text' THEN '{"type": "string", "pattern": "^[^\\r\\n]*$"}'::jsonb
        WHEN 'string' THEN '{"type": "string", "pattern": "^[^\\r\\n]*$"}'::jsonb
        WHEN 'reference' THEN jsonb_build_object('type', 'string', 'format', 'uuid', 'x-reference', COALESCE(d.reference_target, 'ci'))
        WHEN 'file' THEN jsonb_build_object('type', 'string', 'format', 'uuid', 'x-reference', 'document')
        WHEN 'user' THEN '{"type": "string", "format": "uuid", "x-reference": "user"}'::jsonb
        WHEN 'team' THEN '{"type": "string", "format": "uuid", "x-reference": "team"}'::jsonb
        WHEN 'location' THEN '{"type": "string", "format": "uuid", "x-reference": "location"}'::jsonb
        WHEN 'ci_ref' THEN '{"type": "string", "format": "uuid", "x-reference": "ci"}'::jsonb
        WHEN 'asset_ref' THEN '{"type": "string", "format": "uuid", "x-reference": "asset"}'::jsonb
        WHEN 'contract_ref' THEN '{"type": "string", "format": "uuid", "x-reference": "contract"}'::jsonb
        ELSE '{"type": "string"}'::jsonb
    END;
    IF v ? 'min' THEN p := p || jsonb_build_object('minimum', v -> 'min'); END IF;
    IF v ? 'max' THEN p := p || jsonb_build_object('maximum', v -> 'max'); END IF;
    IF v ? 'min_length' THEN p := p || jsonb_build_object('minLength', v -> 'min_length'); END IF;
    IF v ? 'max_length' THEN p := p || jsonb_build_object('maxLength', v -> 'max_length'); END IF;
    IF COALESCE(v ->> 'pattern', '') <> '' THEN p := p || jsonb_build_object('pattern', v -> 'pattern'); END IF;
    IF jsonb_typeof(v -> 'options') = 'array' AND jsonb_array_length(v -> 'options') > 0 AND NOT p ? 'enum' THEN
        p := p || jsonb_build_object('enum', v -> 'options');
    END IF;
    IF d.multi_value AND d.data_type NOT IN ('multi_enum', 'multiselect') THEN
        p := jsonb_build_object('type', 'array', 'items', p);
    END IF;
    p := p || jsonb_build_object('title', COALESCE(d.display_name ->> 'en', d.display_name ->> 'de'),
        'x-display-name', d.display_name, 'x-data-type', d.data_type, 'x-order', d.sort_order,
        'x-searchable', d.searchable, 'x-inheritable', d.inheritable);
    IF COALESCE(d.description, '') <> '' THEN p := p || jsonb_build_object('description', d.description); END IF;
    IF d.default_value IS NOT NULL AND d.default_value <> 'null'::jsonb THEN
        p := p || jsonb_build_object('default', d.default_value);
    END IF;
    IF d.is_unique THEN p := p || jsonb_build_object('x-unique', d.unique_scope); END IF;
    IF COALESCE(d.unit, '') <> '' THEN p := p || jsonb_build_object('x-unit', d.unit); END IF;
    IF COALESCE(d.group_name, '') <> '' THEN p := p || jsonb_build_object('x-group', d.group_name); END IF;
    RETURN p;
END
$$;

DROP FUNCTION compose_attribute_definition(attribute_definition, attribute_definition);
DROP FUNCTION reticora_allowed_values(attribute_definition);
DROP FUNCTION reticora_narrowest(jsonb, jsonb, boolean);
DROP FUNCTION reticora_reference_target(attribute_definition);
DROP FUNCTION reticora_attribute_type(text);

SELECT set_config('reticora.schema_generation', 'on', false);
UPDATE ci_type t SET attribute_schema = generate_attribute_schema(t.id, COALESCE(t.key, t.name)),
    required_fields = ARRAY(SELECT jsonb_array_elements_text(generate_attribute_schema(t.id, COALESCE(t.key, t.name)) -> 'required'));
SELECT set_config('reticora.schema_generation', 'off', false);
