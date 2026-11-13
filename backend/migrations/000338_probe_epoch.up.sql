-- Migration 000338: probe_epoch, the high-water mark of the assignment
-- epoch per probe (PRB-11, WP-941).
--
-- The epoch of probe_assignment is the fencing token of a probe: it must
-- never go down and never be issued twice. An assignment row goes in
-- several ways (the probe is disabled, changes the collector group, its
-- collector leaves the group or is deleted), and with it its epoch.
-- probe_epoch(probe PK, organization, epoch) keeps the highest epoch ever
-- issued for the probe; the assignment engine raises it in the statement
-- that writes the assignment, so a probe assigned again after its
-- assignment went gets a higher epoch than every earlier one. It goes only
-- with its probe (ON DELETE CASCADE): a deleted probe's id is never used
-- again.
--
-- The reference carries organization_id (Konvention 19). ENABLE and FORCE
-- RLS with the organization policy (pattern probe_ingest_cursor, 000311):
-- no API route reads it, only the assignment engine writes it under
-- OrgWideScope. Truncate guard. A normal table; nothing to backfill: the
-- engine takes the stored assignment epoch into account as well.

CREATE TABLE probe_epoch (
    probe_id        uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    epoch           bigint NOT NULL CONSTRAINT probe_epoch_epoch_check CHECK (epoch > 0),
    CONSTRAINT probe_epoch_probe_fkey FOREIGN KEY (probe_id, organization_id)
        REFERENCES probe (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_probe_epoch_org ON probe_epoch (organization_id);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON probe_epoch
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE probe_epoch ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_epoch FORCE ROW LEVEL SECURITY;

CREATE POLICY probe_epoch_isolation ON probe_epoch FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
