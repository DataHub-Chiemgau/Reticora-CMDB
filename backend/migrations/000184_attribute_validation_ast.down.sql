-- Restores the composition functions of 000107, which read the legacy
-- structure only; validations stored as a rule AST are translated into it
-- first (a required rule is kept by the column required).
UPDATE attribute_definition SET validation = reticora_validation_legacy(validation)
WHERE jsonb_typeof(validation) = 'object' AND validation ? 'op';

CREATE OR REPLACE FUNCTION reticora_allowed_values(d attribute_definition) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN jsonb_typeof(d.validation -> 'options') = 'array' AND jsonb_array_length(d.validation -> 'options') > 0
            THEN d.validation -> 'options'
        WHEN jsonb_typeof(d.enum_values) = 'array' AND jsonb_array_length(d.enum_values) > 0 THEN d.enum_values
    END
$$;

CREATE OR REPLACE FUNCTION compose_attribute_definition(b attribute_definition, t attribute_definition) RETURNS attribute_definition
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    r attribute_definition := t;
    bv jsonb := COALESCE(b.validation, '{}'::jsonb);
    tv jsonb := COALESCE(t.validation, '{}'::jsonb);
    v jsonb := '{}'::jsonb;
    ba jsonb := reticora_allowed_values(b);
    ta jsonb := reticora_allowed_values(t);
    allowed jsonb;
    lo jsonb;
    hi jsonb;
    pats text[] := '{}';
    pat text;
BEGIN
    IF reticora_attribute_type(b.data_type) <> reticora_attribute_type(t.data_type)
       OR reticora_reference_target(b) <> reticora_reference_target(t)
       OR b.multi_value <> t.multi_value THEN
        RAISE EXCEPTION 'attribute %: data type % conflicts with % (MET-16)', t.key, t.data_type, b.data_type
            USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition';
    END IF;
    r.required := b.required OR t.required;
    IF t.default_value IS NULL OR t.default_value = 'null'::jsonb THEN
        r.default_value := b.default_value;
    END IF;
    r.description := COALESCE(NULLIF(t.description, ''), b.description);
    r.group_name := COALESCE(NULLIF(t.group_name, ''), b.group_name);
    r.unit := COALESCE(NULLIF(t.unit, ''), b.unit);

    allowed := COALESCE(ba, ta);
    IF ba IS NOT NULL AND ta IS NOT NULL THEN
        SELECT jsonb_agg(e ORDER BY n) INTO allowed
        FROM jsonb_array_elements(ba) WITH ORDINALITY x(e, n) WHERE ta @> jsonb_build_array(e);
        IF allowed IS NULL THEN
            RAISE EXCEPTION 'attribute %: no allowed value is left (MET-16)', t.key
                USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition';
        END IF;
    END IF;

    lo := reticora_narrowest(bv -> 'min', tv -> 'min', true);
    hi := reticora_narrowest(bv -> 'max', tv -> 'max', false);
    IF lo IS NOT NULL AND hi IS NOT NULL AND (lo #>> '{}')::numeric > (hi #>> '{}')::numeric THEN
        RAISE EXCEPTION 'attribute %: the value range is empty (MET-16)', t.key
            USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition';
    END IF;
    IF lo IS NOT NULL THEN v := v || jsonb_build_object('min', lo); END IF;
    IF hi IS NOT NULL THEN v := v || jsonb_build_object('max', hi); END IF;
    lo := reticora_narrowest(bv -> 'min_length', tv -> 'min_length', true);
    hi := reticora_narrowest(bv -> 'max_length', tv -> 'max_length', false);
    IF lo IS NOT NULL AND hi IS NOT NULL AND (lo #>> '{}')::numeric > (hi #>> '{}')::numeric THEN
        RAISE EXCEPTION 'attribute %: the length range is empty (MET-16)', t.key
            USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition';
    END IF;
    IF lo IS NOT NULL THEN v := v || jsonb_build_object('min_length', lo); END IF;
    IF hi IS NOT NULL THEN v := v || jsonb_build_object('max_length', hi); END IF;

    FOR pat IN
        SELECT p FROM (
            SELECT bv ->> 'pattern' AS p, 0 AS n
            UNION ALL SELECT e, n FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(bv -> 'patterns') = 'array'
                THEN bv -> 'patterns' ELSE '[]'::jsonb END) WITH ORDINALITY x(e, n)
            UNION ALL SELECT tv ->> 'pattern', 1000000
            UNION ALL SELECT e, 1000000 + n FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(tv -> 'patterns') = 'array'
                THEN tv -> 'patterns' ELSE '[]'::jsonb END) WITH ORDINALITY x(e, n)
        ) all_patterns WHERE COALESCE(p, '') <> '' ORDER BY n
    LOOP
        IF NOT pat = ANY (pats) THEN
            pats := pats || pat;
        END IF;
    END LOOP;
    IF cardinality(pats) > 0 THEN v := v || jsonb_build_object('pattern', pats[1]); END IF;
    IF cardinality(pats) > 1 THEN v := v || jsonb_build_object('patterns', to_jsonb(pats[2:])); END IF;

    r.enum_values := NULL;
    IF allowed IS NOT NULL THEN
        IF reticora_attribute_type(r.data_type) IN ('enum', 'multiselect') THEN
            r.enum_values := allowed;
        ELSE
            v := v || jsonb_build_object('options', allowed);
        END IF;
    END IF;
    r.validation := v;
    RETURN r;
END
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
    IF jsonb_typeof(v -> 'patterns') = 'array' AND jsonb_array_length(v -> 'patterns') > 0 THEN
        p := p || jsonb_build_object('allOf', (SELECT jsonb_agg(jsonb_build_object('pattern', e))
            FROM jsonb_array_elements(v -> 'patterns') e));
    END IF;
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

DROP FUNCTION reticora_validation_legacy(jsonb);
