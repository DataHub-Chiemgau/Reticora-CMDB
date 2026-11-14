-- Migration 000341: workflow runs are executed one at a time per CI in
-- the order of their events (WFL-02, EVT-01, WP-1089, E-227 option 2).
--
-- workflow_run gets
--   ci_id       the CI the run refers to; composite foreign key with the
--               organization (Konvention 19), ON DELETE SET NULL of the CI
--               column only,
--   serial_key  'ci:<ci_id>' for a run with a CI, NULL otherwise; the job
--               workflow.run executes the runs of one key one after the
--               other,
--   entity_seq  the sequence number of the event of the object that
--               started the run (Envelope.EntitySeq, WP-1090), NULL for a
--               run without one,
--   enqueues    how often the run was queued as a job; the job key is
--               workflow.run:<run_id>:<enqueues>.
-- Policies are unchanged (organization of the run); until WP-1093 the
-- scope of a run follows its CI (rls.DerivedScopeTables, runVisible).

ALTER TABLE workflow_run
    ADD COLUMN ci_id uuid NULL,
    ADD COLUMN serial_key text NULL,
    ADD COLUMN entity_seq bigint NULL,
    ADD COLUMN enqueues integer NOT NULL DEFAULT 0 CONSTRAINT workflow_run_enqueues_check CHECK (enqueues >= 0),
    ADD CONSTRAINT workflow_run_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id);
CREATE INDEX idx_workflow_run_serial ON workflow_run (organization_id, serial_key, status, entity_seq, created_at)
    WHERE serial_key IS NOT NULL;
CREATE INDEX idx_workflow_run_ci ON workflow_run (organization_id, ci_id) WHERE ci_id IS NOT NULL;
