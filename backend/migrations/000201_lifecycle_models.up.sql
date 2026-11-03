-- WP-263 (MET-01, MET-06, LCY-02, E-116): lifecycle models in the
-- metamodel.
-- * The tables lifecycle_definition, lifecycle_state and
--   lifecycle_transition keep their names; "lifecycle model" is the term of
--   API and DTO. ci_type.lifecycle_definition_id becomes lifecycle_model_id;
--   its foreign key has no ON DELETE action any more, so a bound model
--   cannot be deleted while a cascading organization delete still runs.
-- * applies_to is asset or ci. Organization definitions with "both" move
--   to migration_quarantine together with their states, transitions and
--   the ids of the CI types bound to them.
-- * lifecycle_model_effective(type, applies_to) is the model of the type
--   chain (parent_type_id, nearest to the leaf) with a matching applies_to;
--   ci_check_lifecycle_model checks ci.lifecycle_state against the
--   effective ci model. CI states outside it move to migration_quarantine.
-- * System seed "software" (applies_to ci), bound to the system types
--   application and software (E-116).

-- 1. Definitions with applies_to = 'both' (organization rows only; there is
--    no system row with "both").
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT d.organization_id, 'lifecycle_definition',
       jsonb_build_object(
           'definition', to_jsonb(d),
           'states', COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY s.sort_order, s.key)
                               FROM lifecycle_state s WHERE s.definition_id = d.id), '[]'::jsonb),
           'transitions', COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY t.key)
                                    FROM lifecycle_transition t WHERE t.definition_id = d.id), '[]'::jsonb),
           'ci_type_ids', COALESCE((SELECT jsonb_agg(ct.id ORDER BY ct.id)
                                    FROM ci_type ct WHERE ct.lifecycle_definition_id = d.id), '[]'::jsonb)),
       'lifecycle definition with applies_to both; a lifecycle model applies to asset or ci (WP-263)',
       '000201'
FROM lifecycle_definition d
WHERE d.applies_to = 'both' AND d.organization_id IS NOT NULL;

UPDATE ci_type SET lifecycle_definition_id = NULL
WHERE lifecycle_definition_id IN (
    SELECT id FROM lifecycle_definition WHERE applies_to = 'both' AND organization_id IS NOT NULL);
DELETE FROM lifecycle_definition WHERE applies_to = 'both' AND organization_id IS NOT NULL;

-- 2. Column and foreign key.
ALTER TABLE ci_type DROP CONSTRAINT ci_type_lifecycle_definition_fk;
ALTER TABLE ci_type RENAME COLUMN lifecycle_definition_id TO lifecycle_model_id;
ALTER TABLE ci_type ADD CONSTRAINT ci_type_lifecycle_model_fk
    FOREIGN KEY (lifecycle_model_id) REFERENCES lifecycle_definition(id);

-- 3. Version (If-Match, WP-723) and scope.
ALTER TABLE lifecycle_definition ADD COLUMN version BIGINT NOT NULL DEFAULT 1;
ALTER TABLE lifecycle_definition DROP CONSTRAINT lifecycle_definition_applies_to_check;
ALTER TABLE lifecycle_definition ADD CONSTRAINT lifecycle_definition_applies_to_check
    CHECK (applies_to IN ('asset', 'ci'));

-- 4. The effective model of a type: the lifecycle_model_id nearest to the
--    leaf along the type chain (ci_type_ancestors, root → leaf) whose model
--    applies to p_applies_to; NULL without one. RLS applies (invoker).
CREATE FUNCTION lifecycle_model_effective(p_type uuid, p_applies_to text) RETURNS uuid
LANGUAGE sql STABLE SECURITY INVOKER AS $$
    SELECT t.lifecycle_model_id
    FROM ci_type_ancestors(p_type) a
    JOIN ci_type t ON t.id = a.id
    JOIN lifecycle_definition d ON d.id = t.lifecycle_model_id
    WHERE d.applies_to = p_applies_to
    ORDER BY a.level DESC
    LIMIT 1
$$;

-- 5. ci.lifecycle_state is a state of the effective ci model of the type.
CREATE OR REPLACE FUNCTION ci_check_lifecycle_model() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.lifecycle_state IS NULL THEN
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM lifecycle_state s
        WHERE s.definition_id = lifecycle_model_effective(NEW.ci_type_id, 'ci')
          AND s.key = NEW.lifecycle_state
    ) THEN
        RAISE EXCEPTION 'lifecycle state % is not a state of the lifecycle model of the type of CI %',
            NEW.lifecycle_state, NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_lifecycle_requires_model';
    END IF;
    RETURN NEW;
END
$$;

-- 6. CI states outside the effective ci model (pattern of 000090).
WITH invalid AS (
    SELECT c.id, c.organization_id, c.lifecycle_state
    FROM ci c
    WHERE c.lifecycle_state IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM lifecycle_state s
          WHERE s.definition_id = lifecycle_model_effective(c.ci_type_id, 'ci')
            AND s.key = c.lifecycle_state)
), moved AS (
    UPDATE ci SET lifecycle_state = NULL
    FROM invalid
    WHERE ci.id = invalid.id
    RETURNING invalid.organization_id, invalid.id, invalid.lifecycle_state
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'ci', jsonb_build_object('id', id, 'lifecycle_state', lifecycle_state),
       'lifecycle_state without the effective ci lifecycle model of the CI type (WP-263, DB-05)', '000201'
FROM moved;

-- 7. System seed "software" (LCY-02); states and transitions are global.
INSERT INTO lifecycle_definition (id, organization_id, key, name, applies_to, is_system, description)
VALUES ('00000000-0000-0000-0000-000000000102', NULL, 'software', 'Software Lifecycle', 'ci', true,
        'Default lifecycle for software and applications');

INSERT INTO lifecycle_state (organization_id, definition_id, key, label, is_initial, is_terminal, sort_order, required_fields) VALUES
    (NULL, '00000000-0000-0000-0000-000000000102', 'evaluation', 'Evaluation', true, false, 10, '[]'),
    (NULL, '00000000-0000-0000-0000-000000000102', 'approved', 'Approved', false, false, 20, '[]'),
    (NULL, '00000000-0000-0000-0000-000000000102', 'production', 'Production', false, false, 30, '[]'),
    (NULL, '00000000-0000-0000-0000-000000000102', 'deprecated', 'Deprecated', false, false, 40, '[]'),
    (NULL, '00000000-0000-0000-0000-000000000102', 'eol', 'End of Life', false, true, 50, '[]');

INSERT INTO lifecycle_transition (organization_id, definition_id, from_state_id, to_state_id, key, label)
SELECT NULL, '00000000-0000-0000-0000-000000000102', sfrom.id, sto.id,
       sfrom.key || '_to_' || sto.key, sfrom.label || ' → ' || sto.label
FROM lifecycle_state sfrom
JOIN lifecycle_state sto ON sto.definition_id = sfrom.definition_id
WHERE sfrom.definition_id = '00000000-0000-0000-0000-000000000102'
  AND (sfrom.key, sto.key) IN (
      ('evaluation', 'approved'), ('approved', 'production'), ('production', 'deprecated'),
      ('deprecated', 'production'), ('deprecated', 'eol'), ('approved', 'eol'));

-- 8. Binding to the system types application and software (E-116).
UPDATE ci_type SET lifecycle_model_id = '00000000-0000-0000-0000-000000000102'
WHERE organization_id IS NULL AND key IN ('application', 'software');
