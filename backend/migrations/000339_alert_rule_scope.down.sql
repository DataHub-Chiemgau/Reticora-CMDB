-- Reverts 000339: the evaluation state returns to the rule (empty), scope
-- and group_by are dropped.

ALTER TABLE alert_rule
    ADD COLUMN pending_since timestamptz,
    ADD COLUMN last_fired_at timestamptz,
    DROP COLUMN scope,
    DROP COLUMN group_by;
