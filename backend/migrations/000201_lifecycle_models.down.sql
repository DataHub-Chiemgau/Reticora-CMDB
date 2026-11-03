-- Reverts 000201 (WP-263) in reverse order.

-- 8./7. Binding and seed "software". CI states of the software model and
-- states that only the type chain allowed have no model under the rule of
-- 000090 and are cleared.
UPDATE ci SET lifecycle_state = NULL
WHERE lifecycle_state IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM ci_type t JOIN lifecycle_state s ON s.definition_id = t.lifecycle_model_id
      WHERE t.id = ci.ci_type_id AND s.key = ci.lifecycle_state
        AND t.lifecycle_model_id <> '00000000-0000-0000-0000-000000000102');
UPDATE ci_type SET lifecycle_model_id = NULL
WHERE lifecycle_model_id = '00000000-0000-0000-0000-000000000102';
DELETE FROM lifecycle_definition WHERE id = '00000000-0000-0000-0000-000000000102';

-- 2. Column and foreign key (ON DELETE SET NULL as in 000055).
ALTER TABLE ci_type DROP CONSTRAINT ci_type_lifecycle_model_fk;
ALTER TABLE ci_type RENAME COLUMN lifecycle_model_id TO lifecycle_definition_id;
ALTER TABLE ci_type ADD CONSTRAINT ci_type_lifecycle_definition_fk
    FOREIGN KEY (lifecycle_definition_id) REFERENCES lifecycle_definition(id) ON DELETE SET NULL;

-- 5./4. The trigger function of 000090; then the effective model.
CREATE OR REPLACE FUNCTION ci_check_lifecycle_model() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.lifecycle_state IS NULL THEN
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM ci_type t JOIN lifecycle_state s ON s.definition_id = t.lifecycle_definition_id
        WHERE t.id = NEW.ci_type_id AND s.key = NEW.lifecycle_state
    ) THEN
        RAISE EXCEPTION 'lifecycle state % is not a state of the lifecycle model of the type of CI %',
            NEW.lifecycle_state, NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_lifecycle_requires_model';
    END IF;
    RETURN NEW;
END
$$;
DROP FUNCTION lifecycle_model_effective(uuid, text);

-- 3. Scope with "both"; no version.
ALTER TABLE lifecycle_definition DROP CONSTRAINT lifecycle_definition_applies_to_check;
ALTER TABLE lifecycle_definition ADD CONSTRAINT lifecycle_definition_applies_to_check
    CHECK (applies_to IN ('asset', 'ci', 'both'));
ALTER TABLE lifecycle_definition DROP COLUMN version;

-- 1. Definitions with "both", their states, transitions and bindings.
INSERT INTO lifecycle_definition (id, organization_id, key, name, applies_to, is_system, description, created_at, updated_at)
SELECT d.id, d.organization_id, d.key, d.name, d.applies_to, d.is_system, d.description, d.created_at, d.updated_at
FROM migration_quarantine q,
     jsonb_populate_record(NULL::lifecycle_definition, q.row_data -> 'definition') d
WHERE q.migration = '000201' AND q.source_table = 'lifecycle_definition';

INSERT INTO lifecycle_state (id, organization_id, definition_id, key, label, is_initial, is_terminal, sort_order, required_fields)
SELECT s.id, s.organization_id, s.definition_id, s.key, s.label, s.is_initial, s.is_terminal, s.sort_order, s.required_fields
FROM migration_quarantine q,
     jsonb_populate_recordset(NULL::lifecycle_state, q.row_data -> 'states') s
WHERE q.migration = '000201' AND q.source_table = 'lifecycle_definition';

INSERT INTO lifecycle_transition
SELECT tr.*
FROM migration_quarantine q,
     jsonb_populate_recordset(NULL::lifecycle_transition, q.row_data -> 'transitions') tr
WHERE q.migration = '000201' AND q.source_table = 'lifecycle_definition';

UPDATE ci_type t SET lifecycle_definition_id = (q.row_data -> 'definition' ->> 'id')::uuid
FROM migration_quarantine q
WHERE q.migration = '000201' AND q.source_table = 'lifecycle_definition'
  AND q.row_data -> 'ci_type_ids' ? t.id::text;

-- 6. CI states.
UPDATE ci c SET lifecycle_state = q.row_data ->> 'lifecycle_state'
FROM migration_quarantine q
WHERE q.migration = '000201' AND q.source_table = 'ci' AND c.id = (q.row_data ->> 'id')::uuid;

DELETE FROM migration_quarantine WHERE migration = '000201';
