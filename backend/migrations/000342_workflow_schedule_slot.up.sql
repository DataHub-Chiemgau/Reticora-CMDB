-- Migration 000342: schedule triggers of workflows (JOB-02 [P3], WFL-02,
-- WP-1091).
--
-- workflow_run.schedule_slot is the slot of the schedule a run was started
-- for (NULL for every other run). The unique index makes the start of a
-- slot idempotent: two scheduler leaders of the same slot, or a new version
-- of the definition within the slot, store one run (INSERT … ON CONFLICT DO
-- NOTHING). It is on workflow_id, not on the version, for that reason.
-- Policies are unchanged (organization of the run).

ALTER TABLE workflow_run ADD COLUMN schedule_slot timestamptz NULL;
CREATE UNIQUE INDEX uq_workflow_run_schedule_slot ON workflow_run (organization_id, workflow_id, schedule_slot)
    WHERE schedule_slot IS NOT NULL;
