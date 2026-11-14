-- Migration 000317: immutable versions of workflow definitions; a run is
-- bound to the version it started with (WFL-02, WP-1086, E-228 option 1).
--
-- 1. workflow_def gets UNIQUE (id, organization_id) for composite foreign
--    keys (Konvention 19), only when the key is missing.
-- 2. workflow_def_version(organization, workflow, version >= 1, trigger,
--    conditions, actions, created_by NULL, created_at): one row per stored
--    revision of trigger, conditions and actions. A BEFORE-UPDATE trigger
--    refuses every change, a BEFORE-DELETE trigger every delete that is no
--    cascade of workflow_def or organization (pg_trigger_depth() > 1).
--    Definitions are organization-wide: the policy is the one of
--    workflow_def over app.org_id, no scope columns.
-- 3. workflow_def.version (the number of the current version, start 1) and
--    workflow_def.current_version_id (DEFERRABLE INITIALLY DEFERRED: the
--    definition and its first version are created in one transaction).
--    trigger, conditions and actions of workflow_def stay as the copy of
--    the current version, so list and detail read unchanged.
-- 4. Backfill: version 1 of every existing definition from its columns
--    (created_by NULL); every existing run is bound to it.
-- 5. workflow_run.definition_version_id NOT NULL with a composite foreign
--    key to the version.

-- ─── 1. workflow_def key ──────────────────────────────────────────────────────

-- workflow-def-key:begin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'workflow_def'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE workflow_def ADD CONSTRAINT workflow_def_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
-- workflow-def-key:end

-- ─── 2. workflow_def_version ──────────────────────────────────────────────────

CREATE TABLE workflow_def_version (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    workflow_id     uuid NOT NULL,
    version         integer NOT NULL CONSTRAINT workflow_def_version_version_check CHECK (version >= 1),
    trigger         jsonb NOT NULL DEFAULT '{}'::jsonb,
    conditions      jsonb NOT NULL DEFAULT '[]'::jsonb,
    actions         jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_by      uuid NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT workflow_def_version_workflow_version_key UNIQUE (organization_id, workflow_id, version),
    CONSTRAINT workflow_def_version_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT workflow_def_version_workflow_fkey FOREIGN KEY (workflow_id, organization_id)
        REFERENCES workflow_def (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT workflow_def_version_created_by_fkey FOREIGN KEY (created_by, organization_id)
        REFERENCES app_user (id, organization_id) ON DELETE SET NULL (created_by)
);
CREATE INDEX idx_workflow_def_version_created_by ON workflow_def_version (organization_id, created_by) WHERE created_by IS NOT NULL;

-- A stored version never changes; only the cascade of its definition or
-- organization removes it (the FK action runs as a trigger, depth > 1).
-- ON DELETE SET NULL (created_by) of a removed user is the one update the
-- cascade makes and passes for the same reason.
CREATE FUNCTION workflow_def_version_immutable() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF pg_trigger_depth() > 1 THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'a workflow definition version cannot be changed'
        USING ERRCODE = 'check_violation', CONSTRAINT = 'workflow_def_version_immutable';
END
$$;
CREATE TRIGGER workflow_def_version_immutable BEFORE UPDATE OR DELETE ON workflow_def_version
    FOR EACH ROW EXECUTE FUNCTION workflow_def_version_immutable();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON workflow_def_version
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE workflow_def_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_def_version FORCE ROW LEVEL SECURITY;
CREATE POLICY workflow_def_version_tenant_isolation ON workflow_def_version
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);

-- ─── 3. workflow_def.version and current_version_id ───────────────────────────

ALTER TABLE workflow_def
    ADD COLUMN version integer NOT NULL DEFAULT 1 CONSTRAINT workflow_def_version_check CHECK (version >= 1),
    ADD COLUMN current_version_id uuid NULL;

-- ─── 4. backfill ──────────────────────────────────────────────────────────────

INSERT INTO workflow_def_version (organization_id, workflow_id, version, trigger, conditions, actions, created_by, created_at)
SELECT d.organization_id, d.id, 1, d.trigger, d.conditions, d.actions, NULL, d.created_at
  FROM workflow_def d;
UPDATE workflow_def d SET current_version_id = v.id
  FROM workflow_def_version v
 WHERE v.workflow_id = d.id AND v.organization_id = d.organization_id AND v.version = 1;

ALTER TABLE workflow_def
    ALTER COLUMN current_version_id SET NOT NULL,
    ADD CONSTRAINT workflow_def_current_version_fkey FOREIGN KEY (current_version_id, organization_id)
        REFERENCES workflow_def_version (id, organization_id) DEFERRABLE INITIALLY DEFERRED;

-- ─── 5. workflow_run.definition_version_id ────────────────────────────────────

ALTER TABLE workflow_run ADD COLUMN definition_version_id uuid NULL;
UPDATE workflow_run r SET definition_version_id = d.current_version_id
  FROM workflow_def d
 WHERE d.id = r.workflow_id AND d.organization_id = r.organization_id;
ALTER TABLE workflow_run
    ALTER COLUMN definition_version_id SET NOT NULL,
    ADD CONSTRAINT workflow_run_definition_version_fkey FOREIGN KEY (definition_version_id, organization_id)
        REFERENCES workflow_def_version (id, organization_id) ON DELETE CASCADE;
CREATE INDEX idx_workflow_run_definition_version ON workflow_run (organization_id, definition_version_id);
