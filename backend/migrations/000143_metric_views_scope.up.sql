-- WP-350 (TEN-02, TEN-05, TEC-06, MON-01; F-051): the metric views honour
-- the client and site scope (ADR 0001, point 4; decision E-63, option 1).
--
-- metric_sample_v and metric_sample_1h_v (000071) restricted the rows to the
-- organization only. A principal restricted to client 1 or site 1 selecting
-- the views directly read the samples of client-2 CIs; the scope hung on the
-- predicates of each store query alone.
--
-- Both views get the visibility of the sample's CI as a further barrier
-- predicate: a sample of a CI is visible only when the CI's client and site
-- lie in the transaction's scope as the ci policy defines it (TEN-05: NULL =
-- org-wide, visible); a row without a CI stays visible (ci_id is NOT NULL in
-- metric_sample today). The condition is spelled out over
-- app.client_scope/app.site_scope rather than left to the ci policy, so it
-- does not depend on the view owner (the owner bypasses the RLS of ci unless
-- FORCE applies to it). The GUCs are read in sub-selects (InitPlan, evaluated
-- once), and an org-wide transaction skips the CI lookup, so the org-wide
-- read paths keep their plan. metric_sample gets no client_id or site_id
-- (E-63): a CI moving to another client or site changes the visibility of its
-- samples at once, and no compressed chunk is rewritten.
--
-- CREATE OR REPLACE keeps the columns and the grants of 000071 (SELECT,
-- INSERT on metric_sample_v, SELECT on metric_sample_1h_v). The CASCADED
-- CHECK OPTION of metric_sample_v now also refuses an insert for a CI outside
-- the scope.

CREATE OR REPLACE VIEW metric_sample_v WITH (security_barrier) AS
    SELECT s.time, s.organization_id, s.ci_id, s.metric_name, s.value, s.labels
      FROM metric_sample s
     WHERE s.organization_id = (SELECT current_setting('app.org_id')::uuid)
       AND ((SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL
                AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
            OR s.ci_id IS NULL
            OR EXISTS (
                SELECT 1 FROM ci
                 WHERE ci.id = s.ci_id AND ci.organization_id = s.organization_id
                   AND (ci.client_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL)
                        OR ci.client_id = ANY ((SELECT string_to_array(current_setting('app.client_scope', true), ','))::uuid[]))
                   AND (ci.site_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.site_scope', true), '') IS NULL)
                        OR ci.site_id = ANY ((SELECT string_to_array(current_setting('app.site_scope', true), ','))::uuid[]))))
    WITH CASCADED CHECK OPTION;

CREATE OR REPLACE VIEW metric_sample_1h_v WITH (security_barrier) AS
    SELECT a.bucket, a.organization_id, a.ci_id, a.metric_name, a.avg_value, a.min_value, a.max_value, a.sample_count
      FROM metric_sample_1h a
     WHERE a.organization_id = (SELECT current_setting('app.org_id')::uuid)
       AND ((SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL
                AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
            OR a.ci_id IS NULL
            OR EXISTS (
                SELECT 1 FROM ci
                 WHERE ci.id = a.ci_id AND ci.organization_id = a.organization_id
                   AND (ci.client_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL)
                        OR ci.client_id = ANY ((SELECT string_to_array(current_setting('app.client_scope', true), ','))::uuid[]))
                   AND (ci.site_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.site_scope', true), '') IS NULL)
                        OR ci.site_id = ANY ((SELECT string_to_array(current_setting('app.site_scope', true), ','))::uuid[]))));
