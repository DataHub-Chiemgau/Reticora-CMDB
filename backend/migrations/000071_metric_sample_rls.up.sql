-- WP-040 (MON-01, TEC-06): tenant separation for metrics as decided in
-- docs/decisions/0001-timescale-rls.md.
--
-- TimescaleDB 2.17 refuses row level security on hypertables with
-- compression and on continuous aggregates (ADR, result 1). metric_sample
-- and metric_sample_1h therefore keep compression, retention and the
-- aggregate without RLS; the application reaches them only through
-- security-barrier views with the organization predicate as a sub-select
-- (evaluated once, keeps segment pruning):
--
--   metric_sample_v     SELECT, INSERT (CASCADED CHECK OPTION)
--   metric_sample_1h_v  SELECT
--
-- reticora_app loses every direct privilege on the hypertable, the aggregate
-- and their internal relations. Retention, compression and refresh stay
-- TimescaleDB jobs of the owner. New chunks are created with one day each
-- (MON-01); existing chunks keep their interval.

SELECT set_chunk_time_interval('metric_sample', INTERVAL '1 day');

CREATE VIEW metric_sample_v WITH (security_barrier) AS
    SELECT time, organization_id, ci_id, metric_name, value, labels
      FROM metric_sample
     WHERE organization_id = (SELECT current_setting('app.org_id')::uuid)
    WITH CASCADED CHECK OPTION;

CREATE VIEW metric_sample_1h_v WITH (security_barrier) AS
    SELECT bucket, organization_id, ci_id, metric_name, avg_value, min_value, max_value, sample_count
      FROM metric_sample_1h
     WHERE organization_id = (SELECT current_setting('app.org_id')::uuid);

-- Default privileges (migration 000056) granted the new views every
-- privilege; the application gets exactly what the ADR allows.
REVOKE ALL ON metric_sample_v, metric_sample_1h_v FROM reticora_app;
GRANT SELECT, INSERT ON metric_sample_v TO reticora_app;
GRANT SELECT ON metric_sample_1h_v TO reticora_app;

-- Direct privileges on the hypertable, the aggregate and every internal
-- relation behind them (chunks, compressed and materialized hypertables,
-- the aggregate's partial and direct views).
DO $$
DECLARE
    rel RECORD;
BEGIN
    REVOKE ALL ON metric_sample, metric_sample_1h FROM reticora_app;
    FOR rel IN
        SELECT format('%I.%I', h.schema_name, h.table_name) AS name
          FROM _timescaledb_catalog.hypertable h
         WHERE h.id IN (
                SELECT id FROM _timescaledb_catalog.hypertable WHERE table_name = 'metric_sample'
                UNION SELECT compressed_hypertable_id FROM _timescaledb_catalog.hypertable WHERE table_name = 'metric_sample'
                UNION SELECT mat_hypertable_id FROM _timescaledb_catalog.continuous_agg WHERE user_view_name = 'metric_sample_1h')
        UNION ALL
        -- Chunks are found through inheritance: the chunk catalog changed
        -- between TimescaleDB versions (schema/table name columns until 2.2x,
        -- relid since), inheritance did not.
        SELECT i.inhrelid::regclass::text
          FROM pg_inherits i
          JOIN _timescaledb_catalog.hypertable h
            ON i.inhparent = format('%I.%I', h.schema_name, h.table_name)::regclass
         WHERE h.id IN (
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
        EXECUTE format('REVOKE ALL ON %s FROM reticora_app', rel.name);
    END LOOP;
END
$$;
