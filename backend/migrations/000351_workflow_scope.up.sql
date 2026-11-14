-- Migration 000351: client and site of workflow runs, steps and form
-- submissions in row level security (WFL-01, WFL-02, WP-1093, E-09).
--
-- Until now the policies of form_submission, workflow_run and workflow_step
-- checked the organization only (000027); the client scope came from the
-- application (form.submissionVisible, workflow.runVisible) and both tables
-- stood in rls.DerivedScopeTables.
--
-- 1. workflow_run.client_id/site_id follow the CI of the run (pattern
--    000061, read with the scope widened, 000139). A run without CI has
--    none: on INSERT it takes the single client and the single site of a
--    principal restricted to exactly one (the only rows such a principal may
--    write, E-09), otherwise NULL; NULL is visible to org-wide principals
--    only. A CI that leaves the run (deleted, ON DELETE SET NULL) leaves
--    client and site NULL (fail closed).
-- 2. workflow_step.client_id/site_id are those of its run (child table,
--    Konvention 5).
-- 3. form_submission.client_id/site_id follow the CI; without CI the client
--    of the form (form_def.client_id) and no site; for an org-wide form the
--    single client and site of a restricted principal as in 1. An update
--    without CI whose form gives no client, and a CI that leaves the
--    submission, keep the client and site it had: a submission never falls
--    back to NULL, which the policy shows to every client (fail closed).
-- 4. A CI that changes client or site, a form that changes client and a
--    run that changes scope carry their rows along.
-- 5. Policies with client and site scope (pattern 000058/000063/000313);
--    for workflow_run and workflow_step the client predicate keeps NULL
--    rows for org-wide principals only.
-- 6. Keyset index of the run list (WP-1092): (organization_id, created_at
--    DESC, id DESC).

-- ─── 1. Columns ────────────────────────────────────────────────────────────────

ALTER TABLE workflow_run ADD COLUMN client_id uuid NULL, ADD COLUMN site_id uuid NULL;
ALTER TABLE workflow_step ADD COLUMN client_id uuid NULL, ADD COLUMN site_id uuid NULL;
ALTER TABLE form_submission ADD COLUMN client_id uuid NULL, ADD COLUMN site_id uuid NULL;

-- ─── 2. Derivation ─────────────────────────────────────────────────────────────

-- app_scope_single returns the only id of the scope GUC name (app.client_scope
-- or app.site_scope), NULL for an org-wide or a multi-id scope.
CREATE FUNCTION app_scope_single(name text) RETURNS uuid
LANGUAGE plpgsql STABLE
SET search_path = pg_catalog
AS $$
DECLARE
    ids text[] := string_to_array(NULLIF(current_setting(name, true), ''), ',');
BEGIN
    IF cardinality(ids) = 1 THEN
        RETURN ids[1]::uuid;
    END IF;
    RETURN NULL;
END
$$;

CREATE FUNCTION workflow_run_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    IF NEW.ci_id IS NOT NULL THEN
        previous := app_scope_widen();
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
        PERFORM app_scope_restore(previous);
    ELSIF TG_OP = 'INSERT' THEN
        NEW.client_id := app_scope_single('app.client_scope');
        NEW.site_id := app_scope_single('app.site_scope');
    ELSE
        NEW.client_id := NULL;
        NEW.site_id := NULL;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER workflow_run_scope BEFORE INSERT OR UPDATE OF ci_id, client_id, site_id, organization_id ON workflow_run
    FOR EACH ROW EXECUTE FUNCTION workflow_run_scope();

CREATE FUNCTION workflow_step_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    SELECT r.client_id, r.site_id INTO NEW.client_id, NEW.site_id
      FROM workflow_run r WHERE r.id = NEW.run_id AND r.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;
CREATE TRIGGER workflow_step_scope BEFORE INSERT OR UPDATE OF run_id, client_id, site_id, organization_id ON workflow_step
    FOR EACH ROW EXECUTE FUNCTION workflow_step_scope();

CREATE FUNCTION form_submission_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    ELSE
        SELECT f.client_id INTO NEW.client_id
          FROM form_def f WHERE f.id = NEW.form_id AND f.organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    IF NEW.client_id IS NULL AND NEW.ci_id IS NULL AND TG_OP = 'INSERT' THEN
        NEW.client_id := app_scope_single('app.client_scope');
        NEW.site_id := app_scope_single('app.site_scope');
    ELSIF TG_OP = 'UPDATE' AND NEW.ci_id IS NULL AND (OLD.ci_id IS NOT NULL OR NEW.client_id IS NULL) THEN
        -- The CI left the submission (deleted, ON DELETE SET NULL), or the
        -- form is org-wide: the submission keeps the scope it had and never
        -- falls back to NULL, which every client could read (fail closed).
        NEW.client_id := OLD.client_id;
        NEW.site_id := OLD.site_id;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER form_submission_scope BEFORE INSERT OR UPDATE OF ci_id, form_id, client_id, site_id, organization_id ON form_submission
    FOR EACH ROW EXECUTE FUNCTION form_submission_scope();

-- ─── 3. Propagation ────────────────────────────────────────────────────────────

CREATE FUNCTION propagate_workflow_scope_from_ci() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE workflow_run SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    UPDATE form_submission SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER workflow_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_workflow_scope_from_ci();

CREATE FUNCTION propagate_submission_scope_from_form() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE form_submission SET form_id = form_id
     WHERE form_id = NEW.id AND organization_id = NEW.organization_id AND ci_id IS NULL;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER form_submission_scope_propagation AFTER UPDATE ON form_def
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_submission_scope_from_form();

CREATE FUNCTION propagate_workflow_step_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE workflow_step SET run_id = run_id WHERE run_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER workflow_step_scope_propagation AFTER UPDATE ON workflow_run
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_workflow_step_scope();

-- ─── 4. Backfill ───────────────────────────────────────────────────────────────

-- Runs and submissions without CI keep NULL (no principal is known).
UPDATE workflow_run r SET client_id = c.client_id, site_id = c.site_id
  FROM ci c WHERE c.id = r.ci_id AND c.organization_id = r.organization_id;
UPDATE workflow_step s SET client_id = r.client_id, site_id = r.site_id
  FROM workflow_run r WHERE r.id = s.run_id AND r.organization_id = s.organization_id;
UPDATE form_submission s SET client_id = c.client_id, site_id = c.site_id
  FROM ci c WHERE c.id = s.ci_id AND c.organization_id = s.organization_id;
UPDATE form_submission s SET client_id = f.client_id
  FROM form_def f WHERE s.ci_id IS NULL AND f.id = s.form_id AND f.organization_id = s.organization_id;

-- ─── 5. Indexes ────────────────────────────────────────────────────────────────

CREATE INDEX idx_workflow_run_client ON workflow_run (organization_id, client_id);
CREATE INDEX idx_workflow_run_site ON workflow_run (organization_id, site_id);
CREATE INDEX idx_workflow_step_client ON workflow_step (organization_id, client_id);
CREATE INDEX idx_workflow_step_site ON workflow_step (organization_id, site_id);
CREATE INDEX idx_form_submission_client ON form_submission (organization_id, client_id);
CREATE INDEX idx_form_submission_site ON form_submission (organization_id, site_id);
CREATE INDEX idx_workflow_run_created ON workflow_run (organization_id, created_at DESC, id DESC);

-- ─── 6. Row level security ─────────────────────────────────────────────────────

DROP POLICY workflow_run_tenant_isolation ON workflow_run;
DROP POLICY workflow_step_tenant_isolation ON workflow_step;
DROP POLICY form_submission_tenant_isolation ON form_submission;

-- Konvention 5 (E-09): a restricted principal reads the rows of its clients
-- and sites, writes only rows inside its scope. Runs and steps without a
-- client are org-wide (only org-wide principals read them); submissions
-- without a client or site are readable like other org-wide rows. No
-- policy uses EXISTS.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t text;
    client_read text;
BEGIN
    FOREACH t IN ARRAY ARRAY['workflow_run', 'workflow_step', 'form_submission'] LOOP
        client_read := CASE WHEN t = 'form_submission' THEN format('(%s OR client_id IS NULL OR %s)', scope_all, scope_match)
                            ELSE format('(%s OR %s)', scope_all, scope_match) END;
        EXECUTE format('CREATE POLICY %I ON %I USING (%s AND %s AND (%s OR site_id IS NULL OR %s)) '
            || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
            t || '_tenant_isolation', t, org_match, client_read, site_all, site_match,
            org_match, scope_all, scope_match, site_all, site_match);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND (%s OR %s)) '
            || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
            t || '_scoped_update', t, org_match, scope_all, scope_match, site_all, site_match,
            org_match, scope_all, scope_match, site_all, site_match);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND (%s OR %s))',
            t || '_scoped_delete', t, org_match, scope_all, scope_match, site_all, site_match);
    END LOOP;
END
$$;
