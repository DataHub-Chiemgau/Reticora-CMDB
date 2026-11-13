-- Migration 000340 (number provisional): the aggregation tiers of the probe
-- results (PRB-06, NFR-06; WP-972, E-172, ADR 0001, ADR 0002).
--
-- 1. Three hierarchical continuous aggregates, each created WITH NO DATA
--    (golang-migrate runs the migration in one transaction; pattern
--    000020): probe_result_5m on probe_result without the results with
--    stale_epoch, probe_result_1h on probe_result_5m and probe_result_1d on
--    probe_result_1h (day boundaries in UTC: time_bucket without time zone).
--    Every tier keeps counters and sums (total, ok, fail, error by the
--    status encoding 0/1/2 of probe_result.status, rtt_sum/rtt_count/
--    rtt_min/rtt_max, loss_sum/loss_count, value_sum/value_count/value_min/
--    value_max); the upper tiers sum the sums and counters, so the means
--    stay exact. Sums of real columns are double precision (ADR 0002,
--    decision 6); the means exist only in the views.
-- 2. Refresh windows of more than 2 days: a collector buffers results for
--    at least 24 h (PRB-07) and delivers them late; with these windows a
--    result up to 48 h after its measurement time reaches every tier, also
--    in the worst case (ADR 0002, section "Aggregationsstufen"): a bucket
--    is refreshed only when the window covers it whole, the bucket may
--    start up to its width before the measurement, a tier reads the tier
--    below once that one ran, and the job runs once per interval at some
--    phase. A tier with width w, interval i and start_offset s takes a
--    delay L when s >= L + w + i + (latency of the tiers below):
--      5m: start_offset 2 days 1 h, end_offset 5 min, every 5 min
--          (48 h + 5 min + 5 min, with 50 min to spare)
--      1h: start_offset 3 days, end_offset 1 h, hourly
--          (48 h + 1 h + 1 h + 5 min)
--      1d: start_offset 5 days, end_offset 1 day, daily
--          (48 h + 1 day + 1 day + 1 h 5 min)
-- 3. Compression of the materializations (segmentby organization_id,
--    probe_id) beyond the refresh window: 5m after 3 days, 1h after 7 days,
--    1d after 30 days. Retention 5m 90 days, 1h and 1d 400 days (E-172).
--    Refresh, compression and retention run as TimescaleDB jobs of the
--    owner (the migrating role, like probe_result).
-- 4. No row level security (ADR 0001, result 1: TimescaleDB refuses RLS on
--    continuous aggregates). The application reads the tiers only through
--    the security-barrier views probe_result_5m_v, _1h_v and _1d_v with the
--    organization as a sub-select and the client and site of the probe
--    spelled out over app.client_scope and app.site_scope (pattern 000143);
--    an org-wide transaction skips the probe lookup.
-- 5. reticora_app gets SELECT on the three views only. The REVOKE on the
--    aggregates, their materialization hypertables, chunks, compressed chunk
--    relations and partial and direct views runs before any compression is
--    switched on (TimescaleDB 2.31 copies the ACL of the hypertable to every
--    <chunk>_compressed relation; ADR 0002, decision 7).
-- 6. The truncate guard of 000144 on every materialization hypertable (the
--    startup check covers schema public only).

CREATE MATERIALIZED VIEW probe_result_5m
    WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
    SELECT organization_id, probe_id, time_bucket('5 minutes', time) AS bucket,
           count(*) AS total,
           count(*) FILTER (WHERE status = 0) AS ok,
           count(*) FILTER (WHERE status = 1) AS fail,
           count(*) FILTER (WHERE status = 2) AS error,
           sum(rtt_ms::float8) AS rtt_sum, count(rtt_ms) AS rtt_count,
           min(rtt_ms) AS rtt_min, max(rtt_ms) AS rtt_max,
           sum(loss::float8) AS loss_sum, count(loss) AS loss_count,
           sum(value) AS value_sum, count(value) AS value_count,
           min(value) AS value_min, max(value) AS value_max
      FROM probe_result
     WHERE NOT stale_epoch
     GROUP BY organization_id, probe_id, time_bucket('5 minutes', time)
    WITH NO DATA;

CREATE MATERIALIZED VIEW probe_result_1h
    WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
    SELECT organization_id, probe_id, time_bucket('1 hour', bucket) AS bucket,
           sum(total)::bigint AS total, sum(ok)::bigint AS ok,
           sum(fail)::bigint AS fail, sum(error)::bigint AS error,
           sum(rtt_sum) AS rtt_sum, sum(rtt_count)::bigint AS rtt_count,
           min(rtt_min) AS rtt_min, max(rtt_max) AS rtt_max,
           sum(loss_sum) AS loss_sum, sum(loss_count)::bigint AS loss_count,
           sum(value_sum) AS value_sum, sum(value_count)::bigint AS value_count,
           min(value_min) AS value_min, max(value_max) AS value_max
      FROM probe_result_5m
     GROUP BY organization_id, probe_id, time_bucket('1 hour', bucket)
    WITH NO DATA;

CREATE MATERIALIZED VIEW probe_result_1d
    WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
    SELECT organization_id, probe_id, time_bucket('1 day', bucket) AS bucket,
           sum(total)::bigint AS total, sum(ok)::bigint AS ok,
           sum(fail)::bigint AS fail, sum(error)::bigint AS error,
           sum(rtt_sum) AS rtt_sum, sum(rtt_count)::bigint AS rtt_count,
           min(rtt_min) AS rtt_min, max(rtt_max) AS rtt_max,
           sum(loss_sum) AS loss_sum, sum(loss_count)::bigint AS loss_count,
           sum(value_sum) AS value_sum, sum(value_count)::bigint AS value_count,
           min(value_min) AS value_min, max(value_max) AS value_max
      FROM probe_result_1h
     GROUP BY organization_id, probe_id, time_bucket('1 day', bucket)
    WITH NO DATA;

-- Before any compression (ADR 0002, decision 7): the aggregates, their
-- materialization hypertables with every chunk and compressed chunk
-- relation present, and the partial and direct views.
DO $$
DECLARE
    item RECORD;
BEGIN
    REVOKE ALL ON probe_result_5m, probe_result_1h, probe_result_1d FROM reticora_app;
    FOR item IN
        WITH mat AS (
            SELECT format('%I.%I', h.schema_name, h.table_name)::regclass AS relid
              FROM _timescaledb_catalog.continuous_agg a
              JOIN _timescaledb_catalog.hypertable h ON h.id = a.mat_hypertable_id
             WHERE a.user_view_schema = 'public'
               AND a.user_view_name IN ('probe_result_5m', 'probe_result_1h', 'probe_result_1d')),
        chunk AS (
            SELECT i.inhrelid::regclass AS relid FROM pg_inherits i JOIN mat m ON i.inhparent = m.relid)
        SELECT relid::text AS name FROM mat
        UNION ALL SELECT relid::text FROM chunk
        UNION ALL SELECT to_regclass(relid::text || '_compressed')::text FROM chunk
                   WHERE to_regclass(relid::text || '_compressed') IS NOT NULL
        UNION ALL
        SELECT format('%I.%I', a.partial_view_schema, a.partial_view_name)
          FROM _timescaledb_catalog.continuous_agg a
         WHERE a.user_view_schema = 'public'
           AND a.user_view_name IN ('probe_result_5m', 'probe_result_1h', 'probe_result_1d')
        UNION ALL
        SELECT format('%I.%I', a.direct_view_schema, a.direct_view_name)
          FROM _timescaledb_catalog.continuous_agg a
         WHERE a.user_view_schema = 'public'
           AND a.user_view_name IN ('probe_result_5m', 'probe_result_1h', 'probe_result_1d')
    LOOP
        EXECUTE format('REVOKE ALL ON %s FROM reticora_app', item.name);
    END LOOP;
END
$$;

SELECT add_continuous_aggregate_policy('probe_result_5m',
    start_offset => INTERVAL '2 days 1 hour', end_offset => INTERVAL '5 minutes', schedule_interval => INTERVAL '5 minutes');
SELECT add_continuous_aggregate_policy('probe_result_1h',
    start_offset => INTERVAL '3 days', end_offset => INTERVAL '1 hour', schedule_interval => INTERVAL '1 hour');
SELECT add_continuous_aggregate_policy('probe_result_1d',
    start_offset => INTERVAL '5 days', end_offset => INTERVAL '1 day', schedule_interval => INTERVAL '1 day');

ALTER MATERIALIZED VIEW probe_result_5m SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'organization_id, probe_id',
    timescaledb.compress_orderby = 'bucket DESC'
);
ALTER MATERIALIZED VIEW probe_result_1h SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'organization_id, probe_id',
    timescaledb.compress_orderby = 'bucket DESC'
);
ALTER MATERIALIZED VIEW probe_result_1d SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'organization_id, probe_id',
    timescaledb.compress_orderby = 'bucket DESC'
);
SELECT add_compression_policy('probe_result_5m', compress_after => INTERVAL '3 days');
SELECT add_compression_policy('probe_result_1h', compress_after => INTERVAL '7 days');
SELECT add_compression_policy('probe_result_1d', compress_after => INTERVAL '30 days');

SELECT add_retention_policy('probe_result_5m', drop_after => INTERVAL '90 days');
SELECT add_retention_policy('probe_result_1h', drop_after => INTERVAL '400 days');
SELECT add_retention_policy('probe_result_1d', drop_after => INTERVAL '400 days');

-- The truncate guard of 000144 on the three materializations.
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT format('%I.%I', h.schema_name, h.table_name) AS rel
          FROM _timescaledb_catalog.continuous_agg a
          JOIN _timescaledb_catalog.hypertable h ON h.id = a.mat_hypertable_id
         WHERE a.user_view_schema = 'public'
           AND a.user_view_name IN ('probe_result_5m', 'probe_result_1h', 'probe_result_1d')
    LOOP
        EXECUTE format('CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON %s '
                       'FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard()', rec.rel);
    END LOOP;
END
$$;

-- The views of the three tiers: AK2 plus the means (NULL without values).
CREATE VIEW probe_result_5m_v WITH (security_barrier) AS
    SELECT a.organization_id, a.probe_id, a.bucket, a.total, a.ok, a.fail, a.error,
           a.rtt_sum, a.rtt_count, a.rtt_min, a.rtt_max,
           a.loss_sum, a.loss_count, a.value_sum, a.value_count, a.value_min, a.value_max,
           a.rtt_sum / NULLIF(a.rtt_count, 0) AS rtt_avg,
           a.loss_sum / NULLIF(a.loss_count, 0) AS loss_avg,
           a.value_sum / NULLIF(a.value_count, 0) AS value_avg
      FROM probe_result_5m a
     WHERE a.organization_id = (SELECT current_setting('app.org_id')::uuid)
       AND ((SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL
                AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
            OR EXISTS (
                SELECT 1 FROM probe p
                 WHERE p.id = a.probe_id AND p.organization_id = a.organization_id
                   AND (p.client_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL)
                        OR p.client_id = ANY ((SELECT string_to_array(current_setting('app.client_scope', true), ','))::uuid[]))
                   AND (p.site_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.site_scope', true), '') IS NULL)
                        OR p.site_id = ANY ((SELECT string_to_array(current_setting('app.site_scope', true), ','))::uuid[]))));

CREATE VIEW probe_result_1h_v WITH (security_barrier) AS
    SELECT a.organization_id, a.probe_id, a.bucket, a.total, a.ok, a.fail, a.error,
           a.rtt_sum, a.rtt_count, a.rtt_min, a.rtt_max,
           a.loss_sum, a.loss_count, a.value_sum, a.value_count, a.value_min, a.value_max,
           a.rtt_sum / NULLIF(a.rtt_count, 0) AS rtt_avg,
           a.loss_sum / NULLIF(a.loss_count, 0) AS loss_avg,
           a.value_sum / NULLIF(a.value_count, 0) AS value_avg
      FROM probe_result_1h a
     WHERE a.organization_id = (SELECT current_setting('app.org_id')::uuid)
       AND ((SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL
                AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
            OR EXISTS (
                SELECT 1 FROM probe p
                 WHERE p.id = a.probe_id AND p.organization_id = a.organization_id
                   AND (p.client_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL)
                        OR p.client_id = ANY ((SELECT string_to_array(current_setting('app.client_scope', true), ','))::uuid[]))
                   AND (p.site_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.site_scope', true), '') IS NULL)
                        OR p.site_id = ANY ((SELECT string_to_array(current_setting('app.site_scope', true), ','))::uuid[]))));

CREATE VIEW probe_result_1d_v WITH (security_barrier) AS
    SELECT a.organization_id, a.probe_id, a.bucket, a.total, a.ok, a.fail, a.error,
           a.rtt_sum, a.rtt_count, a.rtt_min, a.rtt_max,
           a.loss_sum, a.loss_count, a.value_sum, a.value_count, a.value_min, a.value_max,
           a.rtt_sum / NULLIF(a.rtt_count, 0) AS rtt_avg,
           a.loss_sum / NULLIF(a.loss_count, 0) AS loss_avg,
           a.value_sum / NULLIF(a.value_count, 0) AS value_avg
      FROM probe_result_1d a
     WHERE a.organization_id = (SELECT current_setting('app.org_id')::uuid)
       AND ((SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL
                AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
            OR EXISTS (
                SELECT 1 FROM probe p
                 WHERE p.id = a.probe_id AND p.organization_id = a.organization_id
                   AND (p.client_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL)
                        OR p.client_id = ANY ((SELECT string_to_array(current_setting('app.client_scope', true), ','))::uuid[]))
                   AND (p.site_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.site_scope', true), '') IS NULL)
                        OR p.site_id = ANY ((SELECT string_to_array(current_setting('app.site_scope', true), ','))::uuid[]))));

-- Default privileges (000056) granted the views every privilege; the
-- application gets exactly SELECT.
REVOKE ALL ON probe_result_5m_v, probe_result_1h_v, probe_result_1d_v FROM reticora_app;
GRANT SELECT ON probe_result_5m_v, probe_result_1h_v, probe_result_1d_v TO reticora_app;
