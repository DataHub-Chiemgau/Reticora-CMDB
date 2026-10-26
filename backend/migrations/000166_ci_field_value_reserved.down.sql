-- WP-478: the quarantined rows of the up migration stay in
-- migration_quarantine.
ALTER TABLE ci_field_value DROP CONSTRAINT ci_field_value_field_name_not_reserved;
