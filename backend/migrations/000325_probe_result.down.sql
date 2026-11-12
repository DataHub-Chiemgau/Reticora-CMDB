-- Reverts 000325_probe_result: policies, view and hypertable (with its
-- chunks).

SELECT remove_retention_policy('probe_result', if_exists => true);
SELECT remove_compression_policy('probe_result', if_exists => true);
DROP VIEW IF EXISTS probe_result_v;
DROP TABLE IF EXISTS probe_result;
