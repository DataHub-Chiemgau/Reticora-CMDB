-- Migration 000174: retention of the hourly metric rollups (WP-548, NFR-06;
-- F-317).
--
-- Migration 000020 gave the raw samples (metric_sample) a retention policy
-- of 400 days, the continuous aggregate metric_sample_1h none: its hourly
-- buckets grew without bound. The aggregate gets the same 400 days as a
-- TimescaleDB retention job on its materialization hypertable; like the
-- other policies it runs as a job of the owner.
SELECT add_retention_policy('metric_sample_1h', INTERVAL '400 days', if_not_exists => true);
