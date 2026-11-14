-- WP-985 (ALR-05, MET-45, MON-04, E-201): the condition of an alert rule is
-- a MET-45 condition ($defs/condition of api/rules.schema.json) over the
-- metric context of a series (value, avg, max, min, delta, rate over
-- eval_window), in up to three stages and with a clear condition
-- (hysteresis).
--
-- 1. critical_condition, warning_condition, info_condition: the stages; the
--    first one that holds (critical, warning, info) is the severity of the
--    alarm. At least one is set (CHECK after the data migration).
-- 2. clear_condition: an open alarm is resolved only when it holds; NULL
--    resolves it once no stage holds any more.
-- 3. eval_window: the window of the aggregates (0 to 24 hours).
-- 4. condition, threshold and severity become nullable and stay readable for
--    one release (ci_refresh_health of 000090 reads them until WP-989); the
--    application keeps them set for the rules they can express and NULL for
--    the others. Their CHECKs allow NULL already.
-- 5. Every rule is migrated as it was: {"op": condition, "field": "value",
--    "value": threshold} in the stage of its severity.
--
-- No new table; RLS, FORCE RLS and the policies alert_rule_isolation and
-- alert_rule_system_select stay unchanged.

ALTER TABLE alert_rule
    ADD COLUMN critical_condition jsonb
        CONSTRAINT alert_rule_critical_condition_check CHECK (jsonb_typeof(critical_condition) = 'object'),
    ADD COLUMN warning_condition jsonb
        CONSTRAINT alert_rule_warning_condition_check CHECK (jsonb_typeof(warning_condition) = 'object'),
    ADD COLUMN info_condition jsonb
        CONSTRAINT alert_rule_info_condition_check CHECK (jsonb_typeof(info_condition) = 'object'),
    ADD COLUMN clear_condition jsonb
        CONSTRAINT alert_rule_clear_condition_check CHECK (jsonb_typeof(clear_condition) = 'object'),
    ADD COLUMN eval_window interval NOT NULL DEFAULT interval '5 minutes'
        CONSTRAINT alert_rule_eval_window_check CHECK (eval_window BETWEEN interval '0' AND interval '24 hours');

UPDATE alert_rule SET
    critical_condition = CASE WHEN severity = 'critical'
        THEN jsonb_build_object('op', condition, 'field', 'value', 'value', threshold) END,
    warning_condition = CASE WHEN severity = 'warning'
        THEN jsonb_build_object('op', condition, 'field', 'value', 'value', threshold) END,
    info_condition = CASE WHEN severity = 'info'
        THEN jsonb_build_object('op', condition, 'field', 'value', 'value', threshold) END;

ALTER TABLE alert_rule
    ALTER COLUMN condition DROP NOT NULL,
    ALTER COLUMN threshold DROP NOT NULL,
    ALTER COLUMN severity DROP NOT NULL,
    ALTER COLUMN severity DROP DEFAULT,
    ADD CONSTRAINT alert_rule_stage_check
        CHECK (critical_condition IS NOT NULL OR warning_condition IS NOT NULL OR info_condition IS NOT NULL);
