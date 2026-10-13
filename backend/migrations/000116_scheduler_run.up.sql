-- Migration 000116: state of the system job scheduler (JOB-02, WP-131).
--
-- One row per scheduled kind: the slot it last ran for, so a new leader
-- catches up on the slots missed while no scheduler ran (bounded per kind).
-- System jobs span organizations: the table has no organization_id and is
-- bound to the operator context (app.operator, database.WithOperator) like
-- operator_audit; tenant transactions see and write nothing.

CREATE TABLE scheduler_run (
    kind             TEXT PRIMARY KEY,
    last_due_at      TIMESTAMPTZ NOT NULL,
    last_started_at  TIMESTAMPTZ,
    last_finished_at TIMESTAMPTZ,
    last_error       TEXT,
    runner           TEXT NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE scheduler_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduler_run FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduler_run_operator ON scheduler_run
    USING (current_setting('app.operator', true) = 'on')
    WITH CHECK (current_setting('app.operator', true) = 'on');
