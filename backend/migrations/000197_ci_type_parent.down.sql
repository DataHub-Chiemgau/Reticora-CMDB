-- Down of 000197: the system types lose the category and icon of E-117,
-- the schema functions return to their 000107 versions, the subtypes get
-- the schema of their own definitions again, and triggers, functions,
-- index and column go away.

UPDATE ci_type SET icon = NULL WHERE organization_id IS NULL;
UPDATE ci_type SET category = NULL
WHERE organization_id IS NULL AND key IN ('switch', 'router', 'firewall', 'access_point', 'patch_panel',
    'server', 'hypervisor', 'vm', 'client', 'ip_phone', 'pdu', 'ups', 'nas', 'storage_array', 'printer',
    'camera', 'generic_device');

DROP TRIGGER trg_ci_type_parent_regenerate ON ci_type;
DROP TRIGGER trg_ci_type_parent_guard ON ci_type;
DROP FUNCTION ci_type_parent_regenerate();
DROP FUNCTION ci_type_parent_guard();

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

-- The former subtypes are composed from their own definitions again.
SELECT regenerate_attribute_schema(t.id) FROM ci_type t WHERE t.parent_type_id IS NOT NULL;

DROP FUNCTION reticora_chain_attribute_schema(uuid, uuid, text);
DROP FUNCTION ci_type_subtree(uuid);
DROP FUNCTION ci_type_ancestors(uuid);
DROP INDEX idx_ci_type_parent;
ALTER TABLE ci_type DROP COLUMN parent_type_id;
