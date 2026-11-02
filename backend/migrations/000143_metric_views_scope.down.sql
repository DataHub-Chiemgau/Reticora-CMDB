-- Reverts WP-350: the metric views of 000071 with the organization predicate
-- only. CREATE OR REPLACE keeps the grants.

CREATE OR REPLACE VIEW metric_sample_v WITH (security_barrier) AS
    SELECT time, organization_id, ci_id, metric_name, value, labels
      FROM metric_sample
     WHERE organization_id = (SELECT current_setting('app.org_id')::uuid)
    WITH CASCADED CHECK OPTION;

CREATE OR REPLACE VIEW metric_sample_1h_v WITH (security_barrier) AS
    SELECT bucket, organization_id, ci_id, metric_name, avg_value, min_value, max_value, sample_count
      FROM metric_sample_1h
     WHERE organization_id = (SELECT current_setting('app.org_id')::uuid);
