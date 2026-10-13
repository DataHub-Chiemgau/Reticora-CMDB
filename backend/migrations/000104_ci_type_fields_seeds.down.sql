-- Migration 000104 down: drop the logical seeds (when no CI uses them) and
-- the MET-01 fields; display_name becomes text again (English label).
DELETE FROM ci_type t
WHERE t.organization_id IS NULL
  AND t.key IN ('business_service', 'application', 'database', 'cluster', 'cloud_resource', 'software')
  AND NOT EXISTS (SELECT 1 FROM ci WHERE ci.ci_type_id = t.id);

DROP TRIGGER trg_ci_type_defaults ON ci_type;
DROP FUNCTION ci_type_defaults();
ALTER TABLE ci_type DROP CONSTRAINT ci_type_display_name_i18n;
ALTER TABLE ci_type DROP COLUMN schema_version;
ALTER TABLE ci_type DROP COLUMN is_observable;
ALTER TABLE ci_type ALTER COLUMN display_name DROP NOT NULL;
ALTER TABLE ci_type ALTER COLUMN display_name TYPE TEXT USING COALESCE(display_name ->> 'en', display_name ->> 'de');
