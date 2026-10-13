-- Migration 000107: composition of the effective schema (MET-16, WP-119)
-- and its version on the CI.
--
-- The materialized schema of a type (ci_type.attribute_schema, versioned by
-- schema_version) composes the system base — the global definitions of the
-- system — with the type's definitions (phase 1: base + type chain without
-- inheritance). For the same key: required is OR-ed, the allowed values are
-- intersected, min/max and the length bounds take the narrowest range, all
-- patterns apply (allOf), default and presentation of the type win; a
-- different data type, an empty value set or an empty range is a publish
-- error (constraint attribute_definition_composition). backend/internal/
-- citype/compose.go applies the same rules to the organization's and the
-- instance's layers. Every CI records the schema_version it was written
-- against.

-- The MET-11 type of a (former or specific) data type name.
CREATE FUNCTION reticora_attribute_type(data_type text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE data_type
        WHEN 'textarea' THEN 'multiline'
        WHEN 'multi_enum' THEN 'multiselect'
        WHEN 'string' THEN 'text'
        WHEN 'number' THEN 'decimal'
        WHEN 'user' THEN 'reference'
        WHEN 'team' THEN 'reference'
        WHEN 'location' THEN 'reference'
        WHEN 'ci_ref' THEN 'reference'
        WHEN 'asset_ref' THEN 'reference'
        WHEN 'contract_ref' THEN 'reference'
        ELSE data_type
    END
$$;

-- The target of a reference-typed definition ('' for other types).
CREATE FUNCTION reticora_reference_target(d attribute_definition) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN reticora_attribute_type(d.data_type) <> 'reference' THEN ''
        WHEN d.data_type <> 'reference' THEN regexp_replace(d.data_type, '_ref$', '')
        ELSE COALESCE(NULLIF(d.reference_target, ''), 'ci')
    END
$$;

-- The stricter of two optional numeric bounds.
CREATE FUNCTION reticora_narrowest(a jsonb, b jsonb, lower boolean) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN a IS NULL OR a = 'null'::jsonb THEN b
        WHEN b IS NULL OR b = 'null'::jsonb THEN a
        WHEN lower THEN to_jsonb(greatest((a #>> '{}')::numeric, (b #>> '{}')::numeric))
        ELSE to_jsonb(least((a #>> '{}')::numeric, (b #>> '{}')::numeric))
    END
$$;

-- The allowed values of a definition, NULL for any: the options of the
-- validation take precedence over the enum values (as fieldmeta.Rule).
CREATE FUNCTION reticora_allowed_values(d attribute_definition) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN jsonb_typeof(d.validation -> 'options') = 'array' AND jsonb_array_length(d.validation -> 'options') > 0
            THEN d.validation -> 'options'
        WHEN jsonb_typeof(d.enum_values) = 'array' AND jsonb_array_length(d.enum_values) > 0 THEN d.enum_values
    END
$$;

-- compose_attribute_definition merges the definition t of a narrower layer
-- onto b of a wider one (MET-16).
CREATE FUNCTION compose_attribute_definition(b attribute_definition, t attribute_definition) RETURNS attribute_definition
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

-- The materialized schema of a type: the system base composed with the
-- type's definitions; p_type NULL gives the base alone (a new type).
CREATE OR REPLACE FUNCTION generate_attribute_schema(p_type uuid, p_key text) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE
    props jsonb := '{}'::jsonb;
    req jsonb := '[]'::jsonb;
    k text;
    b attribute_definition;
    t attribute_definition;
    has_base boolean;
    d attribute_definition;
BEGIN
    FOR k IN
        SELECT DISTINCT a.key FROM attribute_definition a
        WHERE (a.organization_id IS NULL AND a.ci_type_id IS NULL) OR a.ci_type_id = p_type
        ORDER BY a.key
    LOOP
        SELECT * INTO b FROM attribute_definition a
        WHERE a.organization_id IS NULL AND a.ci_type_id IS NULL AND a.key = k;
        has_base := FOUND;
        SELECT * INTO t FROM attribute_definition a WHERE a.ci_type_id = p_type AND a.key = k;
        IF FOUND AND has_base THEN
            d := compose_attribute_definition(b, t);
        ELSIF FOUND THEN
            d := t;
        ELSE
            d := b;
        END IF;
        props := props || jsonb_build_object(k, attribute_json_schema(d));
        IF d.required THEN
            req := req || to_jsonb(k);
        END IF;
    END LOOP;
    RETURN jsonb_build_object(
        '$schema', 'https://json-schema.org/draft/2020-12/schema',
        '$id', 'urn:reticora:ci-type:' || p_key,
        'type', 'object',
        'properties', props,
        'required', req);
END
$$;

-- A change of the system base regenerates every type.
CREATE OR REPLACE FUNCTION attribute_definition_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP IN ('UPDATE', 'DELETE') AND OLD.organization_id IS NULL AND OLD.ci_type_id IS NULL)
       OR (TG_OP IN ('INSERT', 'UPDATE') AND NEW.organization_id IS NULL AND NEW.ci_type_id IS NULL) THEN
        PERFORM regenerate_attribute_schema(t.id) FROM ci_type t;
        RETURN NULL;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM regenerate_attribute_schema(OLD.ci_type_id);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM regenerate_attribute_schema(NEW.ci_type_id);
    END IF;
    RETURN NULL;
END
$$;

-- A new type starts with the system base.
CREATE OR REPLACE FUNCTION ci_type_schema_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('reticora.schema_generation', true) IS NOT DISTINCT FROM 'on' THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.attribute_schema := generate_attribute_schema(NULL, COALESCE(NEW.key, NEW.name));
        NEW.required_fields := ARRAY(SELECT jsonb_array_elements_text(NEW.attribute_schema -> 'required'));
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

-- Regenerate every type; a changed schema gets a new version.
SELECT regenerate_attribute_schema(t.id) FROM ci_type t;

-- The CI references the schema version it was written against.
ALTER TABLE ci ADD COLUMN schema_version integer NOT NULL DEFAULT 1;
ALTER TABLE ci DISABLE TRIGGER trg_ci_updated_at;
UPDATE ci c SET schema_version = t.schema_version FROM ci_type t WHERE t.id = c.ci_type_id;
ALTER TABLE ci ENABLE TRIGGER trg_ci_updated_at;

CREATE FUNCTION ci_schema_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.ci_type_id IS DISTINCT FROM OLD.ci_type_id
       OR NEW.attributes IS DISTINCT FROM OLD.attributes THEN
        NEW.schema_version := COALESCE((SELECT t.schema_version FROM ci_type t WHERE t.id = NEW.ci_type_id), 1);
    ELSE
        NEW.schema_version := OLD.schema_version;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ci_schema_version BEFORE INSERT OR UPDATE ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_schema_version();
