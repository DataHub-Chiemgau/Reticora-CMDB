-- WP-478 (GLO-13, CI-10, E-23; F-092): the attribute namespaces _overrides,
-- _observed, _provenance and _instance are no field names. The override
-- endpoint accepted them as the {name} of PUT and DELETE
-- /cis/{id}/fields/{name}/override and wrote a ci_field_value row under the
-- reserved name (the override handler and repository now refuse them).
-- Existing rows with a reserved name move to migration_quarantine before
-- the CHECK constraint is added; the projection trigger then recomputes the
-- namespaces of their CIs. The down migration drops the constraint only:
-- the quarantined rows stay there.

WITH moved AS (
    DELETE FROM ci_field_value
    WHERE field_name IN ('_overrides', '_observed', '_provenance', '_instance')
    RETURNING *
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'ci_field_value', to_jsonb(moved),
       'field_name is a reserved attribute namespace (GLO-13)', '000166'
FROM moved;

ALTER TABLE ci_field_value ADD CONSTRAINT ci_field_value_field_name_not_reserved
    CHECK (field_name NOT IN ('_overrides', '_observed', '_provenance', '_instance'));
