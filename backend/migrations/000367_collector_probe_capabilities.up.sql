-- Migration 000367 (number provisional): the capability report of a
-- collector (PRT-18; WP-966).
--
-- collector.probe_capabilities holds the last report of the heartbeat
-- (POST /api/v1/collectors/{id}/heartbeat or .heartbeat) or of the
-- monitoring alive message (.probe.alive): ICMP socket per address family,
-- bindable UDP ports 67/68, clock synchronization, implemented probe types
-- and detected_at. It is written only when it changes; a heartbeat without
-- it leaves the value (older collectors), NULL means no report yet. The
-- assignment (probe.Assign) gives a probe only to a member that can execute
-- it; a member without report counts as capable. RLS and policies of
-- collector stay as they are.

ALTER TABLE collector ADD COLUMN probe_capabilities jsonb NULL;
COMMENT ON COLUMN collector.probe_capabilities IS
    'last capability report of the collector (probewire.ProbeCapabilities, PRT-18); NULL: none yet';
