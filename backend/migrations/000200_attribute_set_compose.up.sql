-- Migration 000200: attribute sets in the composition (WP-709, MET-16).
--
-- 1. The materialized schema of a type (reticora_chain_attribute_schema,
--    called by generate_attribute_schema for a type and by
--    ci_type_schema_guard for a new one) composes system base → type
--    chain → active attribute sets of the chain: the sets of each type of
--    the chain root → type, the types in chain order, the sets of a type
--    in position order. Of the sets of an ancestor only the set attributes
--    with inheritable count, as for its type attributes (WP-248).
--    compose_attribute_definition folds every key (MET-16); an inactive
--    set is not composed. The organization's global attributes stay a
--    runtime layer of citype.ResolveFields (E-93, E-108; 000107, 000183).
-- 2. Regeneration: a change of a set attribute regenerates every type its
--    set is assigned to and their subtrees (attribute_definition_regenerate),
--    so does a change of attribute_set.is_active
--    (trg_attribute_set_regenerate). A change of ci_type_attribute_set
--    regenerates the type and its subtree at the end of the transaction
--    (deferred constraint trigger): a PUT of the set list deletes and
--    inserts several rows and still counts as one change, and
--    regenerate_attribute_schema raises schema_version once per type and
--    only when the schema changed. CIs reference the version through
--    trg_ci_schema_version (000183).
-- 3. Every type with an assignment and its subtree is regenerated. A
--    composition conflict of assignments made between 000199 and this
--    migration aborts it with the constraint
--    attribute_definition_composition, naming the type and the key.
-- The bodies of reticora_chain_attribute_schema and
-- attribute_definition_regenerate are those of 000199 apart from the set
-- layer. No table, column or policy changes.

-- ─── Materialized schema with the set layer ─────────────────────────────────
-- The schema of a type p_type below p_parent: the system base, the
-- inheritable definitions of the chain root → p_parent, all definitions of
-- p_type, then the active sets of the chain (of an ancestor the
-- inheritable set attributes, of p_type all), folded per key by
-- compose_attribute_definition (MET-16).
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
            SELECT a AS def, 0 AS lvl, 0 AS pos FROM attribute_definition a
            WHERE a.organization_id IS NULL AND a.scope = 'global'
            UNION ALL
            SELECT a, c.level, 0 FROM ci_type_ancestors(p_parent) c
            JOIN attribute_definition a ON a.ci_type_id = c.id
            WHERE a.inheritable
            UNION ALL
            SELECT a, 1000, 0 FROM attribute_definition a WHERE a.ci_type_id = p_type
            UNION ALL
            SELECT a, 2000 + c.level, cs.position FROM ci_type_ancestors(p_parent) c
            JOIN ci_type_attribute_set cs ON cs.ci_type_id = c.id
            JOIN attribute_set s ON s.id = cs.attribute_set_id AND s.is_active
            JOIN attribute_definition a ON a.attribute_set_id = s.id
            WHERE a.inheritable
            UNION ALL
            SELECT a, 3000, cs.position FROM ci_type_attribute_set cs
            JOIN attribute_set s ON s.id = cs.attribute_set_id AND s.is_active
            JOIN attribute_definition a ON a.attribute_set_id = s.id
            WHERE cs.ci_type_id = p_type
        ) x ORDER BY (x.def).key, x.lvl, x.pos
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

-- Regenerates every type the set p_set is assigned to and their subtrees
-- (the types row level security shows).
CREATE FUNCTION regenerate_attribute_set_types(p_set uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM regenerate_attribute_schema(t.id) FROM (
        SELECT DISTINCT st.id FROM ci_type_attribute_set cs
        CROSS JOIN LATERAL ci_type_subtree(cs.ci_type_id) st
        WHERE cs.attribute_set_id = p_set
    ) t;
END
$$;

-- A change of the system base regenerates every type, a change of a type's
-- definition the type and its subtree, a change of a set attribute the
-- types of its set and their subtrees.
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
    IF TG_OP IN ('UPDATE', 'DELETE') AND OLD.attribute_set_id IS NOT NULL THEN
        PERFORM regenerate_attribute_set_types(OLD.attribute_set_id);
    END IF;
    IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND NEW.attribute_set_id IS DISTINCT FROM OLD.attribute_set_id) THEN
        IF NEW.attribute_set_id IS NOT NULL THEN
            PERFORM regenerate_attribute_set_types(NEW.attribute_set_id);
        END IF;
    END IF;
    RETURN NULL;
END
$$;

-- ─── Assignment and activity of a set ───────────────────────────────────────
-- A changed assignment regenerates the type and its subtree when the
-- transaction ends: every row of a replaced list fires, the first call
-- materializes the final list, the others find the schema unchanged.
CREATE FUNCTION ci_type_attribute_set_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM regenerate_attribute_schema(s.id) FROM ci_type_subtree(OLD.ci_type_id) s;
    END IF;
    IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND NEW.ci_type_id IS DISTINCT FROM OLD.ci_type_id) THEN
        PERFORM regenerate_attribute_schema(s.id) FROM ci_type_subtree(NEW.ci_type_id) s;
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER trg_ci_type_attribute_set_regenerate
    AFTER INSERT OR UPDATE OR DELETE ON ci_type_attribute_set
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ci_type_attribute_set_regenerate();

-- Activating or deactivating a set regenerates the types it is assigned to.
CREATE FUNCTION attribute_set_regenerate() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM regenerate_attribute_set_types(NEW.id);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_attribute_set_regenerate AFTER UPDATE OF is_active ON attribute_set
    FOR EACH ROW WHEN (OLD.is_active IS DISTINCT FROM NEW.is_active)
    EXECUTE FUNCTION attribute_set_regenerate();

-- ─── Regenerate the types with sets ─────────────────────────────────────────
DO $$
DECLARE
    t record;
    msg text;
    det text;
    con text;
BEGIN
    FOR t IN
        SELECT DISTINCT st.id, ct.organization_id, COALESCE(ct.key, ct.name) AS key
        FROM ci_type_attribute_set cs
        CROSS JOIN LATERAL ci_type_subtree(cs.ci_type_id) st
        JOIN ci_type ct ON ct.id = st.id
        ORDER BY ct.organization_id, 3, st.id
    LOOP
        BEGIN
            PERFORM regenerate_attribute_schema(t.id);
        EXCEPTION WHEN check_violation THEN
            GET STACKED DIAGNOSTICS msg = MESSAGE_TEXT, det = PG_EXCEPTION_DETAIL, con = CONSTRAINT_NAME;
            IF con IS DISTINCT FROM 'attribute_definition_composition' THEN
                RAISE;
            END IF;
            RAISE EXCEPTION 'ci type % (%) of organization %: %; adjust the attribute sets before migrating',
                t.key, t.id, t.organization_id, msg
                USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition', DETAIL = det;
        END;
    END LOOP;
END
$$;
