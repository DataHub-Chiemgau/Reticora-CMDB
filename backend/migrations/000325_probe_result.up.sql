-- Migration 000325 (number provisional): the raw probe results (PRB-06,
-- PRB-07, MON-01; WP-945, E-171, E-172, ADR 0002).
--
-- 1. probe_result: a narrow TimescaleDB hypertable (time, organization,
--    probe, collector, seq, epoch, status 0 ok / 1 fail / 2 error, code,
--    rtt_ms, loss, value, stale_epoch). The idempotency key
--    UNIQUE (organization_id, probe_id, collector_id, seq, time) makes a
--    repeated delivery (INSERT … ON CONFLICT DO NOTHING) a no-op, also on
--    compressed chunks. The organization is part of the key (deviation from
--    the plan's UNIQUE (probe_id, collector_id, seq, time)): an org-wide
--    transaction skips the probe lookup of the view, so a row of another
--    organization with the same probe, collector, seq and time must not
--    take the key of the real delivery away.
-- 2. Values of ADR 0002 (they override the plan's proposal): 3-hour chunks
--    (decision 2), no default time index but (organization_id, time DESC)
--    and (probe_id, time DESC) (decision 4), compression segmentby
--    organization_id, probe_id and orderby time DESC, collector_id, seq
--    after 1 day (decision 3), retention 14 days (PRB-06). Compression and
--    retention run as TimescaleDB jobs of the owner.
-- 3. No row level security (ADR 0001: TimescaleDB refuses RLS on a
--    hypertable with compression). The application reaches the rows only
--    through the security-barrier view probe_result_v with the organization
--    as a sub-select and the client and site of the probe spelled out over
--    app.client_scope and app.site_scope (pattern 000143, F-051; independent
--    of the view owner); an org-wide transaction skips the probe lookup.
--    WITH CASCADED CHECK OPTION refuses an insert for another organization
--    or a probe outside the scope. probe_result carries no client or site
--    of its own (E-63): a probe moving to another client changes the
--    visibility of its results at once.
-- 4. reticora_app gets SELECT and INSERT on the view only. The REVOKE on the
--    hypertable runs before any compression and is never taken back:
--    TimescaleDB 2.31 copies the ACL of the hypertable to every
--    <chunk>_compressed relation (ADR 0002, decision 7). The loop also
--    revokes on every chunk and every compressed chunk relation present.
-- 5. The truncate guard of 000144.

CREATE TABLE probe_result (
    time            timestamptz NOT NULL,
    organization_id uuid NOT NULL,
    probe_id        uuid NOT NULL,
    collector_id    uuid NOT NULL,
    seq             bigint NOT NULL,
    epoch           bigint NOT NULL,
    status          smallint NOT NULL CONSTRAINT probe_result_status_check CHECK (status IN (0, 1, 2)),
    code            smallint NOT NULL DEFAULT 0,
    rtt_ms          real NULL,
    loss            real NULL,
    value           double precision NULL,
    stale_epoch     boolean NOT NULL DEFAULT false,
    CONSTRAINT probe_result_key UNIQUE (organization_id, probe_id, collector_id, seq, time)
);

COMMENT ON COLUMN probe_result.status IS
    'probewire.Result.status: 0 = ok, 1 = fail, 2 = error (probe.ResultStatusOK, ResultStatusFail, ResultStatusError)';
COMMENT ON COLUMN probe_result.stale_epoch IS
    'the result carried an epoch older than the assignment: stored, but it decides no state (PRB-11)';

-- The event trigger of 000078 hands the new table to reticora_owner
-- (NOLOGIN). Hypertables stay with the migrating role like metric_sample
-- (000078 exempts them): TimescaleDB runs the policy jobs as the owner of
-- the hypertable, and a role without LOGIN cannot run a job.
DO $$
BEGIN
    EXECUTE format('ALTER TABLE probe_result OWNER TO %I', current_user);
END
$$;

SELECT create_hypertable('probe_result', 'time', chunk_time_interval => INTERVAL '3 hours', create_default_indexes => false);

CREATE INDEX idx_probe_result_org_time ON probe_result (organization_id, time DESC);
CREATE INDEX idx_probe_result_probe_time ON probe_result (probe_id, time DESC);

-- Before any compression (ADR 0002, decision 7).
REVOKE ALL ON probe_result FROM reticora_app;

ALTER TABLE probe_result SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'organization_id, probe_id',
    timescaledb.compress_orderby = 'time DESC, collector_id, seq'
);
SELECT add_compression_policy('probe_result', compress_after => INTERVAL '1 day');
SELECT add_retention_policy('probe_result', drop_after => INTERVAL '14 days');

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON probe_result
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE VIEW probe_result_v WITH (security_barrier) AS
    SELECT r.time, r.organization_id, r.probe_id, r.collector_id, r.seq, r.epoch, r.status, r.code,
           r.rtt_ms, r.loss, r.value, r.stale_epoch
      FROM probe_result r
     WHERE r.organization_id = (SELECT current_setting('app.org_id')::uuid)
       AND ((SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL
                AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
            OR EXISTS (
                SELECT 1 FROM probe p
                 WHERE p.id = r.probe_id AND p.organization_id = r.organization_id
                   AND (p.client_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL)
                        OR p.client_id = ANY ((SELECT string_to_array(current_setting('app.client_scope', true), ','))::uuid[]))
                   AND (p.site_id IS NULL
                        OR (SELECT NULLIF(current_setting('app.site_scope', true), '') IS NULL)
                        OR p.site_id = ANY ((SELECT string_to_array(current_setting('app.site_scope', true), ','))::uuid[]))))
    WITH CASCADED CHECK OPTION;

-- Default privileges (000056) granted the view every privilege; the
-- application gets exactly SELECT and INSERT.
REVOKE ALL ON probe_result_v FROM reticora_app;
GRANT SELECT, INSERT ON probe_result_v TO reticora_app;

-- Every internal relation behind the hypertable: chunks through
-- inheritance, compressed chunk relations through their name (TimescaleDB
-- 2.31 keeps no compressed hypertable; ADR 0002, result 5).
DO $$
DECLARE
    rel RECORD;
BEGIN
    FOR rel IN
        SELECT i.inhrelid::regclass::text AS name
          FROM pg_inherits i WHERE i.inhparent = 'probe_result'::regclass
        UNION ALL
        SELECT to_regclass(i.inhrelid::regclass::text || '_compressed')::text
          FROM pg_inherits i
         WHERE i.inhparent = 'probe_result'::regclass
           AND to_regclass(i.inhrelid::regclass::text || '_compressed') IS NOT NULL
    LOOP
        EXECUTE format('REVOKE ALL ON %s FROM reticora_app', rel.name);
    END LOOP;
END
$$;
