-- Metric samples with TimescaleDB hypertable
-- Corresponds to spec Migration 0012

CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE;

CREATE TABLE metric_sample (
    time TIMESTAMPTZ NOT NULL,
    organization_id UUID NOT NULL,
    ci_id UUID NOT NULL,
    metric_name TEXT NOT NULL,
    value DOUBLE PRECISION NOT NULL,
    labels JSONB NOT NULL DEFAULT '{}'
);

-- Convert to hypertable
SELECT create_hypertable('metric_sample', 'time');

-- NOTE: metric_sample intentionally has NO row-level security. TimescaleDB does
-- not support compression/columnstore on RLS-protected hypertables, and all
-- access is mediated by the backend which always filters by organization_id
-- (the column is part of every query and the compression segmentby key).

-- Indexes
CREATE INDEX idx_metric_sample_ci ON metric_sample(ci_id, time DESC);
CREATE INDEX idx_metric_sample_name ON metric_sample(metric_name, time DESC);

-- Compression policy (compress chunks older than 7 days)
ALTER TABLE metric_sample SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'organization_id, ci_id, metric_name'
);
SELECT add_compression_policy('metric_sample', INTERVAL '7 days');

-- Retention policy (drop data older than 400 days)
SELECT add_retention_policy('metric_sample', INTERVAL '400 days');

-- Continuous aggregate for hourly rollup. WITH NO DATA is required because
-- golang-migrate executes each migration inside a transaction, and
-- CREATE MATERIALIZED VIEW ... WITH DATA cannot run in a transaction block.
-- The continuous aggregate policy below refreshes it on schedule.
CREATE MATERIALIZED VIEW metric_sample_hourly
WITH (timescaledb.continuous) AS
SELECT
    time_bucket('1 hour', time) AS bucket,
    organization_id,
    ci_id,
    metric_name,
    avg(value) AS avg_value,
    min(value) AS min_value,
    max(value) AS max_value,
    count(*) AS sample_count
FROM metric_sample
GROUP BY bucket, organization_id, ci_id, metric_name
WITH NO DATA;

SELECT add_continuous_aggregate_policy('metric_sample_hourly',
    start_offset => INTERVAL '3 hours',
    end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '1 hour');
