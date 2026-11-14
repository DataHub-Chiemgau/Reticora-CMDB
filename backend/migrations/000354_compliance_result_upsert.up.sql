-- Migration 000354: compliance results as an upsert per rule and CI
-- (SEC-03, WP-1126, E-237 option 2).
--
-- The evaluation runs as the job compliance.evaluate and per changed CI;
-- it writes one row per (organization, rule, CI) with INSERT ... ON
-- CONFLICT DO UPDATE. Duplicates of older runs are removed (the newest by
-- evaluated_at stays). status_changed_at is when the status of the row
-- last changed; the index on evaluated_at serves the removal of stale
-- results at the end of a full run. Policies, the scope trigger
-- compliance_result_scope (000061) and the truncate guard stay.

DELETE FROM compliance_result r USING compliance_result n
WHERE n.organization_id = r.organization_id AND n.rule_id = r.rule_id AND n.ci_id = r.ci_id
  AND (n.evaluated_at > r.evaluated_at OR (n.evaluated_at = r.evaluated_at AND n.id > r.id));

ALTER TABLE compliance_result
    ADD COLUMN status_changed_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT compliance_result_rule_ci_key UNIQUE (organization_id, rule_id, ci_id);
UPDATE compliance_result SET status_changed_at = evaluated_at;
CREATE INDEX idx_compliance_result_evaluated ON compliance_result (organization_id, evaluated_at);
