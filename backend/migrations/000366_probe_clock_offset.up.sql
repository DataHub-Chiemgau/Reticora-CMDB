-- Migration 000366 (number provisional): the clock correction of a probe
-- result and a state change (PRB-09; WP-962, E-175).
--
-- While a collector group member is degraded for its clock offset (more than
-- 60 s, collector_group_member.clock_offset_ms), the ingest and the state
-- worker shift observed_at of its results and changes by −offset and keep
-- the applied offset in clock_offset_ms; a row without correction has NULL.
-- The order of a collector's data stays (epoch, seq).
--
-- probe_result is a compressed hypertable: ADD COLUMN without a default is
-- allowed there. probe_result_v gets the column at its end; security
-- barrier, check option and the grants of reticora_app (SELECT, INSERT) stay
-- as 000325 set them (CREATE OR REPLACE VIEW keeps the ACL).

-- The clock evaluation of a member beyond the median window (WP-962 AK3):
-- the instances of the server that receive a collector's alive messages in
-- turn share it under the row lock, so every collector.clock_skew event is
-- emitted exactly once. clock_skew_warned: a warning is open;
-- clock_skew_calm / clock_skew_settle: medians in a row below 2 s (while
-- warned and healthy) / below 30 s (while degraded); clock_corrected_at: the
-- last alive message while degraded for its clock (the ingest checks a
-- repeated delivery for another correction until 24 h after it).
ALTER TABLE collector_group_member
    ADD COLUMN clock_skew_warned boolean NOT NULL DEFAULT false,
    ADD COLUMN clock_skew_calm smallint NOT NULL DEFAULT 0
        CONSTRAINT collector_group_member_clock_skew_calm_check CHECK (clock_skew_calm BETWEEN 0 AND 6),
    ADD COLUMN clock_skew_settle smallint NOT NULL DEFAULT 0
        CONSTRAINT collector_group_member_clock_skew_settle_check CHECK (clock_skew_settle BETWEEN 0 AND 6),
    ADD COLUMN clock_corrected_at timestamptz NULL;

ALTER TABLE probe_state_change ADD COLUMN clock_offset_ms integer NULL;
COMMENT ON COLUMN probe_state_change.clock_offset_ms IS
    'clock offset of the collector (ms, collector − server) by which observed_at was corrected; NULL: not corrected (PRB-09)';

ALTER TABLE probe_result ADD COLUMN clock_offset_ms integer NULL;
COMMENT ON COLUMN probe_result.clock_offset_ms IS
    'clock offset of the collector (ms, collector − server) by which time was corrected; NULL: not corrected (PRB-09)';

CREATE OR REPLACE VIEW probe_result_v WITH (security_barrier) AS
    SELECT r.time, r.organization_id, r.probe_id, r.collector_id, r.seq, r.epoch, r.status, r.code,
           r.rtt_ms, r.loss, r.value, r.stale_epoch, r.clock_offset_ms
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
