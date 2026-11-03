-- Down of 000199: the set attributes are deleted first, then the
-- assignment table and its trigger go away, the schema functions and
-- attribute_definition_defaults return to their versions of 000197 and
-- 000105, the checks and unique keys of attribute_definition to their
-- state before 000199, and the column and attribute_set go away.

DELETE FROM attribute_definition WHERE scope = 'set';

DROP TABLE ci_type_attribute_set;
DROP FUNCTION ci_type_attribute_set_binding();

-- reticora_chain_attribute_schema and attribute_definition_regenerate of
-- 000197.

CREATE OR REPLACE FUNCTION reticora_chain_attribute_schema(p_parent uuid, p_type uuid, p_key text) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE
    props jsonb := '{}'::jsonb;
    req jsonb := '[]'::jsonb;
    cur text;
    d attribute_definition;
    l record;
BEGIN
    FOR l IN
        SELECT x.def, (x.def).key AS key FROM (
            SELECT a AS def, 0 AS lvl FROM attribute_definition a
            WHERE a.organization_id IS NULL AND a.ci_type_id IS NULL
            UNION ALL
            SELECT a, c.level FROM ci_type_ancestors(p_parent) c
            JOIN attribute_definition a ON a.ci_type_id = c.id
            WHERE a.inheritable
            UNION ALL
            SELECT a, 1000 FROM attribute_definition a WHERE a.ci_type_id = p_type
        ) x ORDER BY (x.def).key, x.lvl
    LOOP
        IF cur IS NOT DISTINCT FROM l.key THEN
            d := compose_attribute_definition(d, l.def);
            CONTINUE;
        END IF;
        IF cur IS NOT NULL THEN
            props := props || jsonb_build_object(cur, attribute_json_schema(d));
            IF d.required THEN
                req := req || to_jsonb(cur);
            END IF;
        END IF;
        cur := l.key;
        d := l.def;
    END LOOP;
    IF cur IS NOT NULL THEN
        props := props || jsonb_build_object(cur, attribute_json_schema(d));
        IF d.required THEN
            req := req || to_jsonb(cur);
        END IF;
    END IF;
    RETURN jsonb_build_object(
        '$schema', 'https://json-schema.org/draft/2020-12/schema',
        '$id', 'urn:reticora:ci-type:' || p_key,
        'type', 'object',
        'properties', props,
        'required', req);
END
$$;

CREATE OR REPLACE FUNCTION attribute_definition_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP IN ('UPDATE', 'DELETE') AND OLD.organization_id IS NULL AND OLD.ci_type_id IS NULL)
       OR (TG_OP IN ('INSERT', 'UPDATE') AND NEW.organization_id IS NULL AND NEW.ci_type_id IS NULL) THEN
        PERFORM regenerate_attribute_schema(t.id) FROM ci_type t;
        RETURN NULL;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') AND OLD.ci_type_id IS NOT NULL THEN
        PERFORM regenerate_attribute_schema(s.id) FROM ci_type_subtree(OLD.ci_type_id) s;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND NEW.ci_type_id IS NOT NULL THEN
        PERFORM regenerate_attribute_schema(s.id) FROM ci_type_subtree(NEW.ci_type_id) s;
    END IF;
    RETURN NULL;
END
$$;

-- attribute_definition_defaults of 000105.
CREATE OR REPLACE FUNCTION attribute_definition_defaults() RETURNS trigger
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

DROP INDEX attribute_definition_global_key;
DROP INDEX attribute_definition_system_global_key;
CREATE UNIQUE INDEX attribute_definition_global_key ON attribute_definition (organization_id, key)
    WHERE ci_type_id IS NULL AND organization_id IS NOT NULL;
CREATE UNIQUE INDEX attribute_definition_system_global_key ON attribute_definition (key)
    WHERE ci_type_id IS NULL AND organization_id IS NULL;

ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_scope_type_check;
ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_scope_type_check
    CHECK ((scope = 'global') = (ci_type_id IS NULL));
ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_scope_check;
ALTER TABLE attribute_definition ADD CONSTRAINT attribute_definition_scope_check
    CHECK (scope IN ('global', 'type'));

ALTER TABLE attribute_definition DROP CONSTRAINT attribute_definition_attribute_set_id_key_key;
ALTER TABLE attribute_definition DROP COLUMN attribute_set_id;

DROP TABLE attribute_set;
