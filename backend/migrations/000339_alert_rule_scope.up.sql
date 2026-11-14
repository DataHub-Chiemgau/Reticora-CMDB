-- WP-984 (MON-04, ALR-05, E-201): an alert rule is evaluated per series
-- (CI and the values of its group_by labels) inside a scope, and the
-- pending and firing state of a series lives in alarm (WP-981, states
-- pending and open), no longer on the rule.
--
-- 1. scope: the optional keys ci_ids, ci_type_ids (with their subtypes,
--    ci_type_subtree), client_id, site_id and labels (equality per label
--    key); {} is every CI of the organization. The ids are filter values
--    without foreign key: a deleted CI or type matches no series any more
--    and is refused with 422 when the rule is saved next.
-- 2. group_by: at most five label keys that split a CI into series.
-- 3. pending_since and last_fired_at are dropped: no alarm came from a rule
--    before WP-984, so their content is discarded.
--
-- RLS, FORCE RLS and the policies alert_rule_isolation and
-- alert_rule_system_select stay unchanged.

ALTER TABLE alert_rule
    ADD COLUMN scope jsonb NOT NULL DEFAULT '{}'
        CONSTRAINT alert_rule_scope_check CHECK (jsonb_typeof(scope) = 'object'),
    ADD COLUMN group_by text[] NOT NULL DEFAULT '{}'
        CONSTRAINT alert_rule_group_by_check CHECK (cardinality(group_by) <= 5),
    DROP COLUMN pending_since,
    DROP COLUMN last_fired_at;
