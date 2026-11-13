-- WP-988 (MON-04): follow-up actions per alert rule. actions holds the
-- keys notify and webhook ({"enabled": bool}, both on by default); {} is
-- the defaults (every rule before WP-988). The keys ticket (WP-1040) and
-- workflow (WP-1110) are reserved and refused by the API.
--
-- No new table; RLS, FORCE RLS and the policies alert_rule_isolation and
-- alert_rule_system_select stay unchanged.

ALTER TABLE alert_rule
    ADD COLUMN actions jsonb NOT NULL DEFAULT '{}'
        CONSTRAINT alert_rule_actions_check CHECK (jsonb_typeof(actions) = 'object');
