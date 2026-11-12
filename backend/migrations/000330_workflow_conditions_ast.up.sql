-- Migration 000330: workflow conditions become MET-45 condition nodes
-- (WFL-02, WP-1087, E-229 option 1). Pure data migration of the JSONB
-- columns conditions of workflow_def_version and of its copy in
-- workflow_def; no structural change.
--
-- The G1 evaluator knew {field, op, value} with eq (also without op), ne,
-- exists, not_empty, contains and matches. The rule (the same as
-- workflow.MigrateLegacyCondition):
--   eq, ne, contains -> taken 1:1; one whose value is missing, null, an
--   array or an object (G1 compared it as text, "<nil>" for none) has no
--   MET-45 form and is not convertible like an unknown operator
--   a node already in MET-45 form (logic node, in/not_in/gt/gte/lt/lte/
--   empty) -> unchanged, so up after down changes nothing
--   exists, not_empty -> not_empty without value
--   matches, any other operator, a condition without field -> unchanged and
--   the definition is deactivated (active = false), reported per RAISE
--   NOTICE with organization, id and name.
-- Every version is converted in place, so a waiting run keeps evaluating
-- its version; the immutability trigger of 000317 is disabled only for the
-- duration of this migration. A waiting run of a version that still holds a
-- refused condition fails when it resumes instead of deciding silently.

CREATE FUNCTION reticora_wf_legacy_condition(c jsonb) RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    op text;
BEGIN
    -- A node already in MET-45 form stays as it is, so a second run (up
    -- after the no-op down) leaves conditions written since unchanged:
    -- logic nodes and the comparisons G1 did not know (in, not_in, gt,
    -- gte, lt, lte, empty) in their MET-45 shape. eq, ne, contains and
    -- not_empty convert to themselves below.
    IF jsonb_typeof(c) = 'object' AND jsonb_typeof(c->'op') = 'string' THEN
        op := c->>'op';
        IF op IN ('all', 'any', 'not') AND jsonb_typeof(c->'rules') = 'array' THEN
            RETURN c;
        END IF;
        IF jsonb_typeof(c->'field') = 'string' AND btrim(c->>'field') <> ''
           AND ((op = 'empty' AND NOT c ? 'value')
                OR (op IN ('in', 'not_in') AND jsonb_typeof(c->'value') = 'array'
                    AND jsonb_array_length(c->'value') > 0)
                OR (op IN ('gt', 'gte', 'lt', 'lte') AND jsonb_typeof(c->'value') = 'number')) THEN
            RETURN c;
        END IF;
    END IF;
    IF jsonb_typeof(c) IS DISTINCT FROM 'object'
       OR jsonb_typeof(c->'field') IS DISTINCT FROM 'string'
       OR btrim(c->>'field') = '' THEN
        RETURN NULL;
    END IF;
    IF NOT c ? 'op' THEN
        op := 'eq';
    ELSIF jsonb_typeof(c->'op') = 'string' THEN
        op := coalesce(nullif(c->>'op', ''), 'eq');
    ELSE
        RETURN NULL;
    END IF;
    IF op IN ('eq', 'ne', 'contains') THEN
        IF jsonb_typeof(c->'value') IN ('string', 'number', 'boolean') THEN
            RETURN jsonb_build_object('op', op, 'field', c->'field', 'value', c->'value');
        END IF;
        RETURN NULL;
    ELSIF op IN ('exists', 'not_empty') THEN
        RETURN jsonb_build_object('op', 'not_empty', 'field', c->'field');
    END IF;
    RETURN NULL;
END
$$;

-- The converted list: every convertible condition in MET-45 form, every
-- refused one unchanged (a list that is no array stays as it is).
CREATE FUNCTION reticora_wf_legacy_conditions(list jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT CASE WHEN jsonb_typeof(list) <> 'array' THEN list ELSE coalesce(
        (SELECT jsonb_agg(coalesce(reticora_wf_legacy_condition(e.value), e.value) ORDER BY e.ordinality)
           FROM jsonb_array_elements(list) WITH ORDINALITY AS e(value, ordinality)),
        '[]'::jsonb) END
$$;

-- Whether every condition of the list converts.
CREATE FUNCTION reticora_wf_legacy_conditions_ok(list jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT jsonb_typeof(list) = 'array'
       AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(list) AS e(value)
                        WHERE reticora_wf_legacy_condition(e.value) IS NULL)
$$;

DO $$
DECLARE
    d record;
    converted integer := 0;
    deactivated integer := 0;
BEGIN
    FOR d IN
        SELECT organization_id, id, name FROM workflow_def
         WHERE NOT reticora_wf_legacy_conditions_ok(conditions)
         ORDER BY organization_id, id
    LOOP
        RAISE NOTICE 'workflow definition deactivated (condition not convertible to MET-45): organization %, id %, name %',
            d.organization_id, d.id, d.name;
        deactivated := deactivated + 1;
    END LOOP;

    UPDATE workflow_def SET active = false
     WHERE active AND NOT reticora_wf_legacy_conditions_ok(conditions);

    UPDATE workflow_def SET conditions = reticora_wf_legacy_conditions(conditions)
     WHERE conditions IS DISTINCT FROM reticora_wf_legacy_conditions(conditions);
    GET DIAGNOSTICS converted = ROW_COUNT;

    ALTER TABLE workflow_def_version DISABLE TRIGGER workflow_def_version_immutable;
    UPDATE workflow_def_version SET conditions = reticora_wf_legacy_conditions(conditions)
     WHERE conditions IS DISTINCT FROM reticora_wf_legacy_conditions(conditions);
    ALTER TABLE workflow_def_version ENABLE TRIGGER workflow_def_version_immutable;

    RAISE NOTICE 'workflow conditions migrated to MET-45: % definitions converted, % deactivated', converted, deactivated;
END
$$;

DROP FUNCTION reticora_wf_legacy_conditions_ok(jsonb);
DROP FUNCTION reticora_wf_legacy_conditions(jsonb);
DROP FUNCTION reticora_wf_legacy_condition(jsonb);
