-- Database cost of a load run per record (F-118, WP-437), from
-- pg_stat_statements: the statements of the run by execution time with
-- their calls, mean time, shared buffers and rows per call, and the cost of
-- their generic plan (EXPLAIN (GENERIC_PLAN), PostgreSQL 16) as the
-- application role under row level security, with the relations it reads
-- by a sequential scan. The totals divide the run's execution time and
-- buffers by the records of the run, and the plan cost (calls × cost of
-- the generic plan) of the listed statements as well.
--
-- Run it as the owner after the run (pg_stat_statements loaded, reset
-- before the run), with the records of the run and the organization the
-- plans are made for:
--   psql "$DATABASE_URL" -v records=9000 -v org=<uuid> -f tests/load/statement-costs.sql
-- EXPLAIN without ANALYZE executes nothing; a statement without a plan
-- (utility statements, parameters of unknown type) is listed without cost.
\set ON_ERROR_STOP 1
\pset footer off
SELECT set_config('load.org', :'org', false), set_config('load.records', :'records', false) \g /dev/null

CREATE TEMP TABLE load_statements AS
SELECT row_number() OVER (ORDER BY s.total_exec_time DESC) AS rank, s.queryid, s.query, s.calls, s.total_exec_time,
       s.mean_exec_time, s.rows, s.shared_blks_hit + s.shared_blks_read AS blocks,
       NULL::numeric AS plan_cost, NULL::text AS seq_scans
FROM pg_stat_statements s
WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND s.query NOT ILIKE '%pg_stat_statements%'
ORDER BY s.total_exec_time DESC
LIMIT 25;

DO $$
DECLARE
    texts text[];
    costs numeric[] := '{}';
    scans text[] := '{}';
    plan jsonb;
    seq text;
BEGIN
    SELECT array_agg(query ORDER BY rank) INTO texts FROM load_statements;
    -- The plans of the server: the application role, org-wide in the
    -- organization of the run.
    PERFORM set_config('app.org_id', current_setting('load.org'), true);
    PERFORM set_config('app.client_scope', '', true);
    PERFORM set_config('app.site_scope', '', true);
    EXECUTE 'SET LOCAL ROLE reticora_app';
    FOR i IN 1 .. COALESCE(array_length(texts, 1), 0) LOOP
        BEGIN
            EXECUTE 'EXPLAIN (GENERIC_PLAN, FORMAT JSON) ' || texts[i] INTO plan;
            costs[i] := (plan -> 0 -> 'Plan' ->> 'Total Cost')::numeric;
            SELECT string_agg(DISTINCT r #>> '{}', ',') INTO seq
            FROM jsonb_array_elements(jsonb_path_query_array(plan, '$.** ? (@."Node Type" == "Seq Scan")."Relation Name"')) r;
            scans[i] := seq;
        EXCEPTION WHEN OTHERS THEN
            costs[i] := NULL;
            scans[i] := NULL;
        END;
    END LOOP;
    EXECUTE 'RESET ROLE';
    UPDATE load_statements SET plan_cost = costs[rank::int], seq_scans = scans[rank::int];
END
$$;

\echo 'Statements by execution time (generic plan as reticora_app):'
SELECT rank AS "#", calls, round(calls / NULLIF(current_setting('load.records')::numeric, 0), 2) AS "calls/record",
       round(mean_exec_time::numeric, 3) AS "mean ms", round(blocks::numeric / NULLIF(calls, 0), 1) AS "buffers/call",
       round(rows::numeric / NULLIF(calls, 0), 1) AS "rows/call", round(plan_cost, 1) AS "plan cost",
       COALESCE(seq_scans, '') AS "seq scan on", left(regexp_replace(query, '\s+', ' ', 'g'), 140) AS query
FROM load_statements ORDER BY rank;

\echo 'Per record of the run:'
SELECT current_setting('load.records')::bigint AS records,
       sum(s.calls) AS statements,
       round((sum(s.total_exec_time) / NULLIF(current_setting('load.records')::numeric, 0))::numeric, 3) AS "exec ms/record",
       round(sum(s.shared_blks_hit + s.shared_blks_read) / NULLIF(current_setting('load.records')::numeric, 0), 1) AS "buffers/record",
       (SELECT round(sum(calls * plan_cost) / NULLIF(current_setting('load.records')::numeric, 0), 1) FROM load_statements)
           AS "plan cost/record (listed)"
FROM pg_stat_statements s
WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND s.query NOT ILIKE '%pg_stat_statements%';
