-- Down of 000354.
DROP INDEX IF EXISTS idx_compliance_result_evaluated;
ALTER TABLE compliance_result DROP CONSTRAINT IF EXISTS compliance_result_rule_ci_key, DROP COLUMN IF EXISTS status_changed_at;
