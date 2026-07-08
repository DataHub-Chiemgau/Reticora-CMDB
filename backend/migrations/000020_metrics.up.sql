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

-- RLS
ALTER TABLE metric_sample ENABLE ROW LEVEL SECURITY;
ALTER TABLE metric_sample FORCE ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON metric_sample
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Indexes
CREATE INDEX idx_metric_sample_ci ON metric_sample(ci_id, time DESC);
CREATE INDEX idx_metric_sample_name ON metric_sample(metric_name, time DESC);

-- Compression policy (compress chunks older than 7 days)
ALTER TABLE metric_sample SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'organization_id, ci_id, metric_name'
);
SELECT add_compression_policy('metric_sample', INTERVAL '7 days');

-- Retention policy (drop data older than 90 days)
SELECT add_retention_policy('metric_sample', INTERVAL '90 days');

-- Continuous aggregate for hourly rollup
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
GROUP BY bucket, organization_id, ci_id, metric_name;

SELECT add_continuous_aggregate_policy('metric_sample_hourly',
    start_offset => INTERVAL '3 hours',
    end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '1 hour');
