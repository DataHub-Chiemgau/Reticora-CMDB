-- Migration 000105: attribute_definition as the single source of attributes
-- (DB-03, MET-10, WP-116).
--
-- ci_type_attribute becomes attribute_definition (ids, policies and data are
-- kept) with the fields of MET-10: key, display_name (i18n map), data_type,
-- description, required, default (JSON), multi_value, searchable, unique
-- with scope org|client, inheritable, validation, group, order, unit.
-- Organization-global attributes have no CI type (ci_type_id NULL) instead
-- of the synthetic "__global_attributes__" carrier type; system rows have
-- organization_id NULL and stay read-only for the application (TEN-05).
--
-- ci_type.attribute_schema (JSON Schema 2020-12) and required_fields are
-- generated from the type's attribute definitions by the database on every
-- change of a definition, which raises ci_type.schema_version; any other
-- write to them is rejected. Instance attributes (ci_instance_field_
-- definition) stay the per-CI namespace attributes._instance (MET-14, P2).

-- ─── Table and names ─────────────────────────────────────────────────────────
ALTER TABLE ci_type_attribute RENAME TO attribute_definition;
ALTER TABLE attribute_definition RENAME COLUMN name TO key;
ALTER TABLE attribute_definition RENAME COLUMN ui_group TO group_name;
ALTER TABLE attribute_definition RENAME CONSTRAINT ci_type_attribute_pkey TO attribute_definition_pkey;
ALTER TABLE attribute_definition RENAME CONSTRAINT ci_type_attribute_ci_type_id_fkey TO attribute_definition_ci_type_id_fkey;
ALTER TABLE attribute_definition RENAME CONSTRAINT ci_type_attribute_organization_id_fkey TO attribute_definition_organization_id_fkey;
ALTER TABLE attribute_definition RENAME CONSTRAINT ci_type_attribute_ci_type_id_name_key TO attribute_definition_ci_type_id_key_key;
ALTER TABLE attribute_definition RENAME CONSTRAINT ci_type_attribute_scope_check TO attribute_definition_scope_check;
ALTER INDEX idx_ci_type_attribute_org RENAME TO idx_attribute_definition_org;
DROP INDEX idx_ci_type_attribute_global;
ALTER POLICY ci_type_attribute_isolation_select ON attribute_definition RENAME TO attribute_definition_isolation_select;
ALTER POLICY ci_type_attribute_isolation_insert ON attribute_definition RENAME TO attribute_definition_isolation_insert;
ALTER POLICY ci_type_attribute_isolation_update ON attribute_definition RENAME TO attribute_definition_isolation_update;
ALTER POLICY ci_type_attribute_isolation_delete ON attribute_definition RENAME TO attribute_definition_isolation_delete;

-- The organization follows the CI type; a global attribute (no type) keeps
-- the organization it was written with (checked by the policies). A
-- missing display name is the key in both languages.
DROP TRIGGER trg_ci_type_attribute_set_org ON attribute_definition;
DROP FUNCTION ci_type_attribute_set_org();
CREATE FUNCTION attribute_definition_defaults() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.ci_type_id IS NOT NULL THEN
        SELECT ct.organization_id INTO NEW.organization_id FROM ci_type ct WHERE ct.id = NEW.ci_type_id;
    END IF;
    IF NEW.display_name IS NULL OR NEW.display_name = '{}'::jsonb THEN
        NEW.display_name := jsonb_build_object('de', NEW.key, 'en', NEW.key);
    END IF;
    RETURN NEW;
END
$$;

-- ─── MET-10 fields ───────────────────────────────────────────────────────────
CREATE FUNCTION reticora_i18n_text_valid(t jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_typeof(t) = 'object'
       AND t ?| ARRAY['de', 'en']
       AND (t - 'de' - 'en') = '{}'::jsonb
       AND (NOT t ? 'de' OR (jsonb_typeof(t -> 'de') = 'string' AND t ->> 'de' <> ''))
       AND (NOT t ? 'en' OR (jsonb_typeof(t -> 'en') = 'string' AND t ->> 'en' <> ''))
$$;

ALTER TABLE attribute_definition RENAME COLUMN label TO display_name;
ALTER TABLE attribute_definition ALTER COLUMN display_name TYPE JSONB USING
    jsonb_build_object('de', COALESCE(NULLIF(display_name, ''), key), 'en', COALESCE(NULLIF(display_name, ''), key));
ALTER TABLE attribute_definition ALTER COLUMN display_name SET NOT NULL;
ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_display_name_i18n
    CHECK (reticora_i18n_text_valid(display_name));
CREATE TRIGGER trg_attribute_definition_defaults
    BEFORE INSERT OR UPDATE ON attribute_definition
    FOR EACH ROW EXECUTE FUNCTION attribute_definition_defaults();

-- default: any JSON value; former text defaults become JSON strings.
ALTER TABLE attribute_definition ALTER COLUMN default_value TYPE JSONB USING to_jsonb(default_value);

ALTER TABLE attribute_definition
    ADD COLUMN multi_value  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN searchable   BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN is_unique    BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN unique_scope TEXT NOT NULL DEFAULT 'org'
        CONSTRAINT attribute_definition_unique_scope_check CHECK (unique_scope IN ('org', 'client')),
    ADD COLUMN inheritable  BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN unit         TEXT,
    ADD COLUMN updated_at   TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE TRIGGER trg_attribute_definition_updated_at BEFORE UPDATE ON attribute_definition
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─── Global attributes without a carrier type ───────────────────────────────
ALTER TABLE attribute_definition ALTER COLUMN ci_type_id DROP NOT NULL;
UPDATE attribute_definition SET ci_type_id = NULL
WHERE ci_type_id IN (SELECT id FROM ci_type WHERE key = '__global_attributes__');
DELETE FROM ci_type WHERE key = '__global_attributes__';
UPDATE attribute_definition SET scope = 'global' WHERE ci_type_id IS NULL;
ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_scope_type_check
    CHECK ((scope = 'global') = (ci_type_id IS NULL));
CREATE UNIQUE INDEX attribute_definition_global_key ON attribute_definition (organization_id, key)
    WHERE ci_type_id IS NULL AND organization_id IS NOT NULL;
CREATE UNIQUE INDEX attribute_definition_system_global_key ON attribute_definition (key)
    WHERE ci_type_id IS NULL AND organization_id IS NULL;

-- ─── Generated attribute_schema (JSON Schema 2020-12) ───────────────────────

-- attribute_json_schema is the JSON Schema of one attribute.
CREATE FUNCTION attribute_json_schema(d attribute_definition) RETURNS jsonb
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
        WHEN 'ip' THEN '{"type": "string", "anyOf": [{"format": "ipv4"}, {"format": "ipv6"}]}'::jsonb
        WHEN 'mac' THEN '{"type": "string", "pattern": "^([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}$"}'::jsonb
        WHEN 'url' THEN '{"type": "string", "format": "uri"}'::jsonb
        WHEN 'email' THEN '{"type": "string", "format": "email"}'::jsonb
        WHEN 'json' THEN '{}'::jsonb
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
    IF d.multi_value AND d.data_type <> 'multi_enum' THEN
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

-- generate_attribute_schema is the JSON Schema of a CI type: its attribute
-- definitions (the composition with base and type chain follows MET-16).
CREATE FUNCTION generate_attribute_schema(p_type uuid, p_key text) RETURNS jsonb
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

-- regenerate_attribute_schema materializes the schema of a type and raises
-- its schema_version when the schema changed.
CREATE FUNCTION regenerate_attribute_schema(p_type uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    cur jsonb;
    type_key text;
    next jsonb;
BEGIN
    IF p_type IS NULL THEN
        RETURN;
    END IF;
    SELECT attribute_schema, COALESCE(key, name) INTO cur, type_key FROM ci_type WHERE id = p_type;
    IF NOT FOUND THEN
        RETURN; -- the type is being deleted
    END IF;
    next := generate_attribute_schema(p_type, type_key);
    IF cur IS DISTINCT FROM next THEN
        PERFORM set_config('reticora.schema_generation', 'on', true);
        UPDATE ci_type SET attribute_schema = next,
            required_fields = ARRAY(SELECT jsonb_array_elements_text(next -> 'required')),
            schema_version = CASE WHEN cur IS NULL THEN schema_version ELSE schema_version + 1 END
        WHERE id = p_type;
        PERFORM set_config('reticora.schema_generation', 'off', true);
    END IF;
END
$$;

CREATE FUNCTION attribute_definition_regenerate() RETURNS trigger
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
CREATE TRIGGER trg_attribute_definition_regenerate
    AFTER INSERT OR UPDATE OR DELETE ON attribute_definition
    FOR EACH ROW EXECUTE FUNCTION attribute_definition_regenerate();

-- The generated columns are read-only: a new type starts with the empty
-- schema, every other write is the generator's.
CREATE FUNCTION ci_type_schema_guard() RETURNS trigger
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
CREATE TRIGGER trg_ci_type_schema_guard BEFORE INSERT OR UPDATE ON ci_type
    FOR EACH ROW EXECUTE FUNCTION ci_type_schema_guard();

-- Initial generation for every type.
SELECT set_config('reticora.schema_generation', 'on', false);
UPDATE ci_type t SET attribute_schema = generate_attribute_schema(t.id, COALESCE(t.key, t.name)),
    required_fields = ARRAY(SELECT jsonb_array_elements_text(generate_attribute_schema(t.id, COALESCE(t.key, t.name)) -> 'required'));
SELECT set_config('reticora.schema_generation', 'off', false);
