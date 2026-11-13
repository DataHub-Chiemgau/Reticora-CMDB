-- Migration 000361: decisions on workflow approvals with approver circle and
-- quorum (WFL-02, RBA-05, NTF-05, WP-1098, E-230 option 2).
--
-- 1. UNIQUE (id, organization_id) on workflow_run and workflow_step for the
--    composite foreign keys (Konvention 19), each only when it is missing.
-- 2. workflow_approval: one row per decision of an approver on an approval
--    step (decision approved or rejected, comment, decided_at), at most one
--    per step and approver. client_id and site_id are those of the run
--    (BEFORE trigger workflow_approval_scope, the parent row, columns of
--    000351) and follow a run that changes scope. RLS and FORCE RLS with the
--    policy workflow_approval_isolation over app.org_id, app.client_scope
--    and app.site_scope in USING and WITH CHECK, without EXISTS (Konvention
--    5, pattern 000058/000063), and the restrictive workflow_approval_scoped_
--    update/_delete of 000351 (E-09); like the runs a decision without a
--    client is visible to org-wide principals only. Truncate guard (000144).
--    A decision is inserted only with its approver (the trigger refuses a
--    NULL approver_id, which the unique key would not catch); ON DELETE SET
--    NULL of a deleted user keeps the decision afterwards.
-- 3. workflow_step.due_at and reminded_at: deadline and reminder of an
--    approval step (evaluated by WP-1099), index on the waiting steps.

-- ─── 1. keys ──────────────────────────────────────────────────────────────────

-- workflow-run-step-key:begin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['workflow_run', 'workflow_step'] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint u
             WHERE u.conrelid = t::regclass AND u.contype IN ('u', 'p')
               AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                     WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
        ) THEN
            EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I UNIQUE (id, organization_id)', t, t || '_id_organization_key');
        END IF;
    END LOOP;
END
$$;
-- workflow-run-step-key:end

-- ─── 2. workflow_approval ─────────────────────────────────────────────────────

CREATE TABLE workflow_approval (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    run_id          uuid NOT NULL,
    step_id         uuid NOT NULL,
    approver_id     uuid NULL,
    decision        text NOT NULL CONSTRAINT workflow_approval_decision_check CHECK (decision IN ('approved', 'rejected')),
    comment         text NOT NULL DEFAULT '',
    decided_at      timestamptz NOT NULL DEFAULT now(),
    client_id       uuid NULL,
    site_id         uuid NULL,
    CONSTRAINT workflow_approval_step_approver_key UNIQUE (organization_id, step_id, approver_id),
    CONSTRAINT workflow_approval_run_fkey FOREIGN KEY (run_id, organization_id)
        REFERENCES workflow_run (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT workflow_approval_step_fkey FOREIGN KEY (step_id, organization_id)
        REFERENCES workflow_step (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT workflow_approval_approver_fkey FOREIGN KEY (approver_id, organization_id)
        REFERENCES app_user (id, organization_id) ON DELETE SET NULL (approver_id)
);
CREATE INDEX idx_workflow_approval_run ON workflow_approval (organization_id, run_id);
CREATE INDEX idx_workflow_approval_approver ON workflow_approval (organization_id, approver_id) WHERE approver_id IS NOT NULL;
CREATE INDEX idx_workflow_approval_client ON workflow_approval (organization_id, client_id);
CREATE INDEX idx_workflow_approval_site ON workflow_approval (organization_id, site_id);

-- Client and site of a decision are those of its run (the parent row),
-- read with the scope widened (000139).
CREATE FUNCTION workflow_approval_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    -- Every decision names its approver: NULLs are distinct in the unique
    -- key, an anonymous decision would count again and again (four eyes).
    -- Only ON DELETE SET NULL of a deleted user clears it later.
    IF TG_OP = 'INSERT' AND NEW.approver_id IS NULL THEN
        RAISE EXCEPTION 'workflow_approval needs an approver' USING ERRCODE = '23502';
    END IF;
    SELECT r.client_id, r.site_id INTO NEW.client_id, NEW.site_id
      FROM workflow_run r WHERE r.id = NEW.run_id AND r.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;
CREATE TRIGGER workflow_approval_scope BEFORE INSERT OR UPDATE OF run_id, client_id, site_id, organization_id ON workflow_approval
    FOR EACH ROW EXECUTE FUNCTION workflow_approval_scope();

-- A run that changes client or site (its CI moved) carries its decisions
-- along, as its steps (000351).
CREATE FUNCTION propagate_workflow_approval_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE workflow_approval SET run_id = run_id WHERE run_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER workflow_approval_scope_propagation AFTER UPDATE ON workflow_run
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_workflow_approval_scope();

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON workflow_approval
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE workflow_approval ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_approval FORCE ROW LEVEL SECURITY;
CREATE POLICY workflow_approval_isolation ON workflow_approval
    USING (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
-- A principal restricted to clients or sites changes and deletes only rows
-- inside its scope, never an org-wide one (E-09, pattern 000351).
CREATE POLICY workflow_approval_scoped_update ON workflow_approval AS RESTRICTIVE FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY workflow_approval_scoped_delete ON workflow_approval AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));

-- ─── 3. deadline and reminder of approval steps ───────────────────────────────

ALTER TABLE workflow_step ADD COLUMN due_at timestamptz NULL, ADD COLUMN reminded_at timestamptz NULL;
CREATE INDEX idx_workflow_step_due ON workflow_step (organization_id, due_at) WHERE status = 'waiting_approval';
