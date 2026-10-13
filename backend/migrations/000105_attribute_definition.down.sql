-- Migration 000105 down: back to ci_type_attribute with the synthetic
-- global carrier types. System-global attributes (no type, no
-- organization) did not exist before and are removed.

DROP TRIGGER trg_ci_type_schema_guard ON ci_type;
DROP FUNCTION ci_type_schema_guard();
DROP TRIGGER trg_attribute_definition_regenerate ON attribute_definition;
DROP FUNCTION attribute_definition_regenerate();
DROP FUNCTION regenerate_attribute_schema(uuid);
DROP FUNCTION generate_attribute_schema(uuid, text);
DROP FUNCTION attribute_json_schema(attribute_definition);
UPDATE ci_type SET attribute_schema = NULL, required_fields = NULL;

DROP INDEX attribute_definition_system_global_key;
DROP INDEX attribute_definition_global_key;
ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_scope_type_check;
DELETE FROM attribute_definition WHERE ci_type_id IS NULL AND organization_id IS NULL;
INSERT INTO ci_type (organization_id, key, name, display_name, is_active)
SELECT DISTINCT organization_id, '__global_attributes__', '__global_attributes__',
    '{"de": "Globale Attribute", "en": "Global Attributes"}'::jsonb, false
FROM attribute_definition WHERE ci_type_id IS NULL;
UPDATE attribute_definition d SET ci_type_id = ct.id
FROM ci_type ct
WHERE d.ci_type_id IS NULL AND ct.organization_id = d.organization_id AND ct.key = '__global_attributes__';
ALTER TABLE attribute_definition ALTER COLUMN ci_type_id SET NOT NULL;

DROP TRIGGER trg_attribute_definition_updated_at ON attribute_definition;
ALTER TABLE attribute_definition
    DROP COLUMN multi_value,
    DROP COLUMN searchable,
    DROP COLUMN is_unique,
    DROP COLUMN unique_scope,
    DROP COLUMN inheritable,
    DROP COLUMN unit,
    DROP COLUMN updated_at;

ALTER TABLE attribute_definition ALTER COLUMN default_value TYPE TEXT USING
    CASE WHEN jsonb_typeof(default_value) = 'string' THEN default_value #>> '{}' ELSE default_value::text END;

DROP TRIGGER trg_attribute_definition_defaults ON attribute_definition;
DROP FUNCTION attribute_definition_defaults();
ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_display_name_i18n;
ALTER TABLE attribute_definition ALTER COLUMN display_name DROP NOT NULL;
ALTER TABLE attribute_definition ALTER COLUMN display_name TYPE TEXT USING COALESCE(display_name ->> 'en', display_name ->> 'de');
ALTER TABLE attribute_definition RENAME COLUMN display_name TO label;
DROP FUNCTION reticora_i18n_text_valid(jsonb);



ALTER POLICY attribute_definition_isolation_select ON attribute_definition RENAME TO ci_type_attribute_isolation_select;
ALTER POLICY attribute_definition_isolation_insert ON attribute_definition RENAME TO ci_type_attribute_isolation_insert;
ALTER POLICY attribute_definition_isolation_update ON attribute_definition RENAME TO ci_type_attribute_isolation_update;
ALTER POLICY attribute_definition_isolation_delete ON attribute_definition RENAME TO ci_type_attribute_isolation_delete;
ALTER INDEX idx_attribute_definition_org RENAME TO idx_ci_type_attribute_org;
ALTER TABLE attribute_definition RENAME CONSTRAINT attribute_definition_scope_check TO ci_type_attribute_scope_check;
ALTER TABLE attribute_definition RENAME CONSTRAINT attribute_definition_ci_type_id_key_key TO ci_type_attribute_ci_type_id_name_key;
ALTER TABLE attribute_definition RENAME CONSTRAINT attribute_definition_organization_id_fkey TO ci_type_attribute_organization_id_fkey;
ALTER TABLE attribute_definition RENAME CONSTRAINT attribute_definition_ci_type_id_fkey TO ci_type_attribute_ci_type_id_fkey;
ALTER TABLE attribute_definition RENAME CONSTRAINT attribute_definition_pkey TO ci_type_attribute_pkey;
ALTER TABLE attribute_definition RENAME COLUMN group_name TO ui_group;
ALTER TABLE attribute_definition RENAME COLUMN key TO name;
ALTER TABLE attribute_definition RENAME TO ci_type_attribute;
CREATE UNIQUE INDEX idx_ci_type_attribute_global
    ON ci_type_attribute(ci_type_id, name) WHERE scope = 'global';

CREATE FUNCTION ci_type_attribute_set_org() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT ct.organization_id INTO NEW.organization_id FROM ci_type ct WHERE ct.id = NEW.ci_type_id;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ci_type_attribute_set_org
    BEFORE INSERT OR UPDATE OF ci_type_id, organization_id ON ci_type_attribute
    FOR EACH ROW EXECUTE FUNCTION ci_type_attribute_set_org();
