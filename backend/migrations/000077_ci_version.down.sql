DROP INDEX idx_ci_change_ci_version;
ALTER TABLE ci_change DROP COLUMN ci_version;
ALTER TABLE ci DROP COLUMN version;
