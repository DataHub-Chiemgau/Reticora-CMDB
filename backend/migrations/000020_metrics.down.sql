DROP MATERIALIZED VIEW IF EXISTS metric_sample_hourly;
DROP POLICY IF EXISTS org_isolation ON metric_sample;
DROP TABLE IF EXISTS metric_sample;
-- Note: TimescaleDB extension is not dropped as other tables may depend on it
