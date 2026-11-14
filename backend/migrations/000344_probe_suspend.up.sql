-- Migration 000344 (provisional number): suspension of probes whose
-- monitoring feature was lost by a plan change, a cancellation or an expiry
-- (PKG-09, ENT-08, WP-942, E-156 option 2).
--
-- suspended_reason and suspended_at are independent of enabled: the system
-- job probe.entitlement_reconcile sets them when the feature of the probe
-- type is no longer active and clears them when it is again; enabled (the
-- user's switch) stays as it was. A suspended probe is neither assigned
-- nor distributed. RLS, policies and truncate guard of probe stay as they
-- are (000307).

ALTER TABLE probe
    ADD COLUMN suspended_reason text NULL CONSTRAINT probe_suspended_reason_check CHECK (suspended_reason IN ('entitlement')),
    ADD COLUMN suspended_at timestamptz NULL;

CREATE INDEX idx_probe_suspended ON probe (organization_id) WHERE suspended_reason IS NOT NULL;
