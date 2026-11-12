-- Migration 000323: backfill of the management addresses (IPM-13, WP-913,
-- E-215 option 1), without schema change.
--
-- The job ipam.mgmt_backfill (ipam.ManagementSync.RunBackfillJob) brings
-- every CI with a management IP into the state the write path keeps from
-- now on: its address in the VRF of the management IP has role
-- management. It runs per organization in batches of 500 CIs and is
-- idempotent. It is queued once per organization that has a CI with a
-- management IP (pattern 000162); an organization without has nothing to
-- do.

INSERT INTO job (organization_id, kind, params, max_attempts, timeout_seconds)
SELECT o.id, 'ipam.mgmt_backfill', '{}'::jsonb, 3, 3600
  FROM organization o
 WHERE EXISTS (SELECT 1 FROM ci c WHERE c.organization_id = o.id AND c.management_ip IS NOT NULL AND c.deleted_at IS NULL)
   AND NOT EXISTS (SELECT 1 FROM job j WHERE j.organization_id = o.id AND j.kind = 'ipam.mgmt_backfill' AND j.status = 'queued');
