-- Migration 000332: compliance rules become MET-45 conditions (SEC-03,
-- CMP-02, WP-1125, E-229 option 1).
--
-- 1. compliance_rule.applies_when JSONB NULL: an optional MET-45 condition
--    (NULL applies always); false is not_applicable.
-- 2. Data migration of expression, one G1 condition {field, op, value} of
--    the old evaluator, in the rule of workflow conditions (000330):
--      eq (also without op), ne, contains -> taken 1:1 (value must be a
--      string, number or boolean, else not convertible)
--      a node already in MET-45 form (logic node, in/not_in/gt/gte/lt/lte/
--      empty) -> unchanged; a rule already deactivated by this migration
--      (note in description) is not touched again, so up after down
--      changes nothing
--      exists, not_empty -> not_empty without value
--      matches, any other operator, an expression without field -> the rule
--      is deactivated (active = false), keeps the old expression, which is
--      appended to its description, and is reported per RAISE NOTICE with
--      id and name; a NOTICE counts converted and deactivated rules.

ALTER TABLE compliance_rule ADD COLUMN applies_when jsonb NULL;

-- compliance-rule-ast:begin
CREATE FUNCTION reticora_cmp_legacy_condition(c jsonb) RETURNS jsonb
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

DO $$
DECLARE
    r record;
    converted integer := 0;
    deactivated integer := 0;
BEGIN
    FOR r IN SELECT id, name FROM compliance_rule
              WHERE reticora_cmp_legacy_condition(expression) IS NULL
                AND coalesce(description, '') NOT LIKE '%Deactivated by migration 000332 (E-229)%'
              ORDER BY organization_id, id LOOP
        RAISE NOTICE 'compliance rule deactivated (expression not convertible to MET-45): id %, name %', r.id, r.name;
    END LOOP;

    UPDATE compliance_rule
       SET active = false,
           description = concat_ws(E'\n\n', nullif(description, ''),
                                   'Deactivated by migration 000332 (E-229): the expression is no MET-45 condition. Legacy expression: ' || expression::text)
     WHERE reticora_cmp_legacy_condition(expression) IS NULL
       AND coalesce(description, '') NOT LIKE '%Deactivated by migration 000332 (E-229)%';
    GET DIAGNOSTICS deactivated = ROW_COUNT;

    UPDATE compliance_rule SET expression = reticora_cmp_legacy_condition(expression)
     WHERE reticora_cmp_legacy_condition(expression) IS NOT NULL
       AND expression IS DISTINCT FROM reticora_cmp_legacy_condition(expression);
    GET DIAGNOSTICS converted = ROW_COUNT;

    RAISE NOTICE 'compliance rules migrated to MET-45: % converted, % deactivated', converted, deactivated;
END
$$;

DROP FUNCTION reticora_cmp_legacy_condition(jsonb);
-- compliance-rule-ast:end
