-- Migration 000198: the composition conflicts carry a machine-readable code
-- (WP-711, MET-16).
--
-- compose_attribute_definition (000107, last replaced in 000184) reports
-- every conflict as check_violation under the constraint
-- attribute_definition_composition. Each RAISE now names its rule in
-- DETAIL, with the same code as citype.CompositionError.Code:
-- datatype-conflict (data type, reference target or multi_value differ),
-- enum-empty (no allowed value is left), range-empty (min > max) and
-- length-range-empty (min_length > max_length). The body is otherwise the
-- one of 000184; messages, schemas and schema_version stay unchanged,
-- nothing is regenerated. No table, column, RLS or policy changes.
--
-- The backend now treats multi_value as part of the data type in the
-- organization's layer too (citype.checkCompositionTx, ci.resolveFields),
-- as the function always did between the system base and a type. An
-- organization-global attribute and a type attribute of the same key that
-- differ only in multi_value went unnoticed before; afterwards every write
-- to a CI of that type would be rejected. The migration therefore refuses
-- to run while such a pair exists and names organization, type and key:
-- the operator aligns multi_value first. The type attributes of the
-- organization's own types count, and those of the system types
-- (organization_id NULL), since the backend composes the organization's
-- globals over both.

DO $$
DECLARE
    pair record;
BEGIN
    SELECT g.organization_id AS org, t.ci_type_id AS type_id, g.key INTO pair
    FROM attribute_definition g
    JOIN attribute_definition t
      ON t.key = g.key AND t.scope = 'type'
     AND (t.organization_id = g.organization_id OR t.organization_id IS NULL)
     AND t.multi_value <> g.multi_value
    WHERE g.scope = 'global' AND g.organization_id IS NOT NULL
    ORDER BY g.organization_id, t.ci_type_id, g.key
    LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'attribute %: multi_value of the global attribute of organization % conflicts with ci type % (MET-16); align multi_value before migrating',
            pair.key, pair.org, pair.type_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition',
                  DETAIL = 'datatype-conflict';
    END IF;
END
$$;

-- compose_attribute_definition of 000184 with DETAIL = '<code>' at each
-- RAISE.
CREATE OR REPLACE FUNCTION compose_attribute_definition(b attribute_definition, t attribute_definition) RETURNS attribute_definition
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    r attribute_definition := t;
    bv jsonb := COALESCE(reticora_validation_legacy(b.validation), '{}'::jsonb);
    tv jsonb := COALESCE(reticora_validation_legacy(t.validation), '{}'::jsonb);
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
            USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition',
                  DETAIL = 'datatype-conflict';
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
                USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition',
                      DETAIL = 'enum-empty';
        END IF;
    END IF;

    lo := reticora_narrowest(bv -> 'min', tv -> 'min', true);
    hi := reticora_narrowest(bv -> 'max', tv -> 'max', false);
    IF lo IS NOT NULL AND hi IS NOT NULL AND (lo #>> '{}')::numeric > (hi #>> '{}')::numeric THEN
        RAISE EXCEPTION 'attribute %: the value range is empty (MET-16)', t.key
            USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition',
                  DETAIL = 'range-empty';
    END IF;
    IF lo IS NOT NULL THEN v := v || jsonb_build_object('min', lo); END IF;
    IF hi IS NOT NULL THEN v := v || jsonb_build_object('max', hi); END IF;
    lo := reticora_narrowest(bv -> 'min_length', tv -> 'min_length', true);
    hi := reticora_narrowest(bv -> 'max_length', tv -> 'max_length', false);
    IF lo IS NOT NULL AND hi IS NOT NULL AND (lo #>> '{}')::numeric > (hi #>> '{}')::numeric THEN
        RAISE EXCEPTION 'attribute %: the length range is empty (MET-16)', t.key
            USING ERRCODE = 'check_violation', CONSTRAINT = 'attribute_definition_composition',
                  DETAIL = 'length-range-empty';
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
