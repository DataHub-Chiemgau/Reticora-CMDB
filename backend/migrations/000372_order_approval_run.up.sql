-- Migration 000372: the order approval as a workflow (WFL-04, WP-1108).
--
-- 1. workflow_run gets UNIQUE (id, organization_id) when it lacks it
--    (workflow_run_id_organization_key, convention 19); the down leaves it.
-- 2. internal_order.approval_run_id names the run of the active definition
--    with the trigger order_submitted that the submit of the order started:
--    composite foreign key on workflow_run, ON DELETE SET NULL
--    (approval_run_id), partial index.
-- 3. At most one active definition with the trigger order_submitted per
--    organization (uq_workflow_def_order_submitted on the stored column
--    workflow_def.trigger_type): a second activation is a unique violation
--    (409).
--
-- 4. workflow_run_scope places the approval run of an order (trigger
--    order_submitted, no CI) in the client of its order, read with the
--    scope widened from the order named by context.order_id, instead of
--    the single client of the submitter's scope: the approvers of that
--    client see and decide it, also when an organization-wide user
--    submitted the order.
--
-- No new table; the row level security of internal_order and workflow_def
-- is unchanged.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workflow_run_id_organization_key'
                   AND conrelid = 'workflow_run'::regclass) THEN
        ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_id_organization_key UNIQUE (id, organization_id);
    END IF;
END $$;

ALTER TABLE internal_order ADD COLUMN approval_run_id UUID NULL;
ALTER TABLE internal_order ADD CONSTRAINT internal_order_approval_run_fkey
    FOREIGN KEY (approval_run_id, organization_id) REFERENCES workflow_run (id, organization_id) ON DELETE SET NULL (approval_run_id);
CREATE INDEX idx_internal_order_approval_run ON internal_order (organization_id, approval_run_id) WHERE approval_run_id IS NOT NULL;

-- The trigger type is a stored column: an index under row level security
-- may use no function that is not leakproof (SR-04/K22, ->> is not).
ALTER TABLE workflow_def ADD COLUMN trigger_type text GENERATED ALWAYS AS (trigger->>'type') STORED;
CREATE UNIQUE INDEX uq_workflow_def_order_submitted ON workflow_def (organization_id)
    WHERE active AND trigger_type = 'order_submitted';

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
    ELSIF NEW.trigger = 'order_submitted' AND NEW.context ? 'order_id' THEN
        previous := app_scope_widen();
        NEW.client_id := (SELECT o.client_id FROM internal_order o
          WHERE o.id::text = NEW.context->>'order_id' AND o.organization_id = NEW.organization_id);
        PERFORM app_scope_restore(previous);
        -- An order has no site: the run keeps the single site of the
        -- submitter's scope, as before.
        NEW.site_id := CASE WHEN TG_OP = 'INSERT' THEN app_scope_single('app.site_scope') ELSE NEW.site_id END;
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
