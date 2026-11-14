-- Reverts 000348: the rules that the old columns cannot express (no
-- condition, threshold or severity: several stages, a clear condition or a
-- condition other than one comparison of value) move to
-- migration_quarantine (pattern 000090); then the stages, the clear
-- condition and eval_window are dropped and condition, threshold and
-- severity are NOT NULL again.

WITH moved AS (
    DELETE FROM alert_rule
     WHERE condition IS NULL OR threshold IS NULL OR severity IS NULL
    RETURNING *
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT organization_id, 'alert_rule', to_jsonb(moved),
       'alert rule without condition, threshold and severity (stages of WP-985)', '000348'
  FROM moved;

ALTER TABLE alert_rule
    DROP CONSTRAINT alert_rule_stage_check,
    DROP COLUMN critical_condition,
    DROP COLUMN warning_condition,
    DROP COLUMN info_condition,
    DROP COLUMN clear_condition,
    DROP COLUMN eval_window,
    ALTER COLUMN condition SET NOT NULL,
    ALTER COLUMN threshold SET NOT NULL,
    ALTER COLUMN severity SET NOT NULL,
    ALTER COLUMN severity SET DEFAULT 'warning';
