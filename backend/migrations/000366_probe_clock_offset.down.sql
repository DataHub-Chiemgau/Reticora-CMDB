-- Reverts 000366_probe_clock_offset: the view of 000325 without the column
-- (a column cannot leave a view through CREATE OR REPLACE, so the view is
-- recreated with the grants of 000325), then the columns.

DROP VIEW probe_result_v;
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
REVOKE ALL ON probe_result_v FROM reticora_app;
GRANT SELECT, INSERT ON probe_result_v TO reticora_app;

ALTER TABLE probe_result DROP COLUMN clock_offset_ms;
ALTER TABLE probe_state_change DROP COLUMN clock_offset_ms;
ALTER TABLE collector_group_member
    DROP COLUMN clock_skew_warned,
    DROP COLUMN clock_skew_calm,
    DROP COLUMN clock_skew_settle,
    DROP COLUMN clock_corrected_at;
