-- Migration 000352: event triggers of workflows (WFL-02, WFL-03, WP-1090,
-- E-226 option 2).
--
-- workflow_run gets
--   trigger_event_id  the event (Envelope.ID) that started the run; one run
--                     per definition version and event
--                     (uq_workflow_run_trigger_event), a repeated delivery
--                     starts none,
--   trigger_depth     how many runs started by events of runs precede the
--                     run (loop protection, at most 3).
-- event_outbox gets origin: the workflow run whose execution wrote the
-- event ({"kind": "workflow_run", "run_id": ..., "depth": ...}); NULL for
-- every other writer. Policies are unchanged.

ALTER TABLE workflow_run
    ADD COLUMN trigger_event_id uuid NULL,
    ADD COLUMN trigger_depth smallint NOT NULL DEFAULT 0 CONSTRAINT workflow_run_trigger_depth_check CHECK (trigger_depth >= 0);
CREATE UNIQUE INDEX uq_workflow_run_trigger_event ON workflow_run (organization_id, definition_version_id, trigger_event_id)
    WHERE trigger_event_id IS NOT NULL;

ALTER TABLE event_outbox ADD COLUMN origin jsonb NULL;
