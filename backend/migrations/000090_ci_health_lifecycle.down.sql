DROP FUNCTION ci_refresh_health(interval);
DROP TRIGGER trg_ci_health_read_only ON ci;
DROP FUNCTION ci_health_read_only();
ALTER TABLE ci DROP COLUMN health_updated_at;
ALTER TABLE ci DROP COLUMN health;
DROP TRIGGER trg_ci_lifecycle_model ON ci;
DROP FUNCTION ci_check_lifecycle_model();

UPDATE ci c SET lifecycle_state = q.row_data ->> 'lifecycle_state'
FROM migration_quarantine q
WHERE q.migration = '000090' AND q.source_table = 'ci' AND c.id = (q.row_data ->> 'id')::uuid;
DELETE FROM migration_quarantine WHERE migration = '000090';
