-- Down of 000372. The key workflow_run_id_organization_key stays (earlier
-- migrations reference it).

DROP INDEX IF EXISTS uq_workflow_def_order_submitted;
ALTER TABLE workflow_def DROP COLUMN IF EXISTS trigger_type;
DROP INDEX IF EXISTS idx_internal_order_approval_run;
ALTER TABLE internal_order DROP CONSTRAINT IF EXISTS internal_order_approval_run_fkey;
ALTER TABLE internal_order DROP COLUMN IF EXISTS approval_run_id;

CREATE OR REPLACE FUNCTION workflow_run_scope() RETURNS trigger
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
