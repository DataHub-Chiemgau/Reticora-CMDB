-- Reverts WP-040: direct privileges for the application role again, no
-- views, seven-day chunks for new data.

DROP VIEW IF EXISTS metric_sample_1h_v;
DROP VIEW IF EXISTS metric_sample_v;

GRANT SELECT, INSERT, UPDATE, DELETE ON metric_sample, metric_sample_1h TO reticora_app;

DO $$
DECLARE
    rel RECORD;
BEGIN
    FOR rel IN
        SELECT format('%I.%I', h.schema_name, h.table_name) AS name
          FROM _timescaledb_catalog.hypertable h
         WHERE h.id IN (
                SELECT id FROM _timescaledb_catalog.hypertable WHERE table_name = 'metric_sample'
                UNION SELECT compressed_hypertable_id FROM _timescaledb_catalog.hypertable WHERE table_name = 'metric_sample'
                UNION SELECT mat_hypertable_id FROM _timescaledb_catalog.continuous_agg WHERE user_view_name = 'metric_sample_1h')
        UNION ALL
        SELECT format('%I.%I', c.schema_name, c.table_name)
          FROM _timescaledb_catalog.chunk c
         WHERE c.hypertable_id IN (
                SELECT id FROM _timescaledb_catalog.hypertable WHERE table_name = 'metric_sample'
                UNION SELECT compressed_hypertable_id FROM _timescaledb_catalog.hypertable WHERE table_name = 'metric_sample'
                UNION SELECT mat_hypertable_id FROM _timescaledb_catalog.continuous_agg WHERE user_view_name = 'metric_sample_1h')
        UNION ALL
        SELECT format('%I.%I', a.partial_view_schema, a.partial_view_name)
          FROM _timescaledb_catalog.continuous_agg a WHERE a.user_view_name = 'metric_sample_1h'
        UNION ALL
        SELECT format('%I.%I', a.direct_view_schema, a.direct_view_name)
          FROM _timescaledb_catalog.continuous_agg a WHERE a.user_view_name = 'metric_sample_1h'
    LOOP
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO reticora_app', rel.name);
    END LOOP;
END
$$;

SELECT set_chunk_time_interval('metric_sample', INTERVAL '7 days');
