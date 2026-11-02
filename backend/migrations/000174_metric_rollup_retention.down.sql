-- Migration 000174 down: the hourly metric rollups lose their retention
-- policy again (state of migration 000020).
SELECT remove_retention_policy('metric_sample_1h', if_exists => true);
