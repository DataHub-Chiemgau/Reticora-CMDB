-- Down of 000200: the regeneration triggers of the sets go away,
-- reticora_chain_attribute_schema and attribute_definition_regenerate
-- return word for word to their versions of 000199 (WP-250), and every
-- type with an assignment and its subtree is regenerated without the set
-- layer. schema_version does not go down: a changed schema gets the next
-- version.

DROP TRIGGER trg_attribute_set_regenerate ON attribute_set;
DROP FUNCTION attribute_set_regenerate();
DROP TRIGGER trg_ci_type_attribute_set_regenerate ON ci_type_attribute_set;
DROP FUNCTION ci_type_attribute_set_regenerate();

-- reticora_chain_attribute_schema and attribute_definition_regenerate of
-- 000199.

-- The schema of a type p_type below p_parent: the system base, the
-- inheritable definitions of the chain root → p_parent and all definitions
-- of p_type, folded per key by compose_attribute_definition (MET-16).
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
            WHERE a.organization_id IS NULL AND a.scope = 'global'
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

-- A change of the system base regenerates every type, a change of a type's
-- definition the type and its subtree; a set attribute regenerates nothing
-- (WP-709).
CREATE OR REPLACE FUNCTION attribute_definition_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP IN ('UPDATE', 'DELETE') AND OLD.organization_id IS NULL AND OLD.scope = 'global')
       OR (TG_OP IN ('INSERT', 'UPDATE') AND NEW.organization_id IS NULL AND NEW.scope = 'global') THEN
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

DROP FUNCTION regenerate_attribute_set_types(uuid);

SELECT regenerate_attribute_schema(t.id) FROM (
    SELECT DISTINCT st.id FROM ci_type_attribute_set cs
    CROSS JOIN LATERAL ci_type_subtree(cs.ci_type_id) st
) t;
