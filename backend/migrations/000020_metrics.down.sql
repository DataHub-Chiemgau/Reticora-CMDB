DROP MATERIALIZED VIEW IF EXISTS metric_sample_hourly;
DROP TABLE IF EXISTS metric_sample;
-- Note: TimescaleDB extension is not dropped as other tables may depend on it
