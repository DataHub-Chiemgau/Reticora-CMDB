-- Reverts 000340: policies, views and aggregates in reverse order (1d, 1h,
-- 5m). Dropping an aggregate removes its materialization, its chunks, the
-- truncate guard on it and its jobs; the policies are removed explicitly
-- first so the order is visible.

DROP VIEW IF EXISTS probe_result_1d_v;
DROP VIEW IF EXISTS probe_result_1h_v;
DROP VIEW IF EXISTS probe_result_5m_v;

SELECT remove_retention_policy('probe_result_1d', if_exists => true);
SELECT remove_compression_policy('probe_result_1d', if_exists => true);
SELECT remove_continuous_aggregate_policy('probe_result_1d', if_exists => true);
SELECT remove_retention_policy('probe_result_1h', if_exists => true);
SELECT remove_compression_policy('probe_result_1h', if_exists => true);
SELECT remove_continuous_aggregate_policy('probe_result_1h', if_exists => true);
SELECT remove_retention_policy('probe_result_5m', if_exists => true);
SELECT remove_compression_policy('probe_result_5m', if_exists => true);
SELECT remove_continuous_aggregate_policy('probe_result_5m', if_exists => true);

DROP MATERIALIZED VIEW IF EXISTS probe_result_1d;
DROP MATERIALIZED VIEW IF EXISTS probe_result_1h;
DROP MATERIALIZED VIEW IF EXISTS probe_result_5m;
