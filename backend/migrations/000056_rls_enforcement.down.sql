DROP INDEX IF EXISTS ci_type_system_key_uniq;
DROP INDEX IF EXISTS ci_type_org_key_uniq;

ALTER TABLE composition DROP CONSTRAINT IF EXISTS composition_child_asset_tenant_fkey;
ALTER TABLE composition DROP CONSTRAINT IF EXISTS composition_child_ci_tenant_fkey;
ALTER TABLE composition DROP CONSTRAINT IF EXISTS composition_parent_tenant_fkey;
ALTER TABLE ci_relationship DROP CONSTRAINT IF EXISTS ci_relationship_target_tenant_fkey;
ALTER TABLE ci_relationship DROP CONSTRAINT IF EXISTS ci_relationship_source_tenant_fkey;
ALTER TABLE asset DROP CONSTRAINT IF EXISTS asset_id_organization_key;
ALTER TABLE ci DROP CONSTRAINT IF EXISTS ci_id_organization_key;

DROP TRIGGER IF EXISTS trg_audit_log_no_update ON audit_log;
DROP TRIGGER IF EXISTS trg_audit_log_no_delete ON audit_log;
DROP FUNCTION IF EXISTS reject_audit_log_mutation();

-- Migration 000056 down: revert database-level tenant isolation enforcement
-- and the system lifecycle repair.

-- Restore the organization-scoped lifecycle policies and re-own the seeded
-- system rows by the placeholder organization.
UPDATE lifecycle_state
SET organization_id = '00000000-0000-0000-0000-000000000001'
WHERE organization_id IS NULL;

UPDATE lifecycle_transition
SET organization_id = '00000000-0000-0000-0000-000000000001'
WHERE organization_id IS NULL;

DROP POLICY IF EXISTS lifecycle_state_isolation ON lifecycle_state;
CREATE POLICY lifecycle_state_isolation ON lifecycle_state
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS lifecycle_transition_isolation ON lifecycle_transition;
CREATE POLICY lifecycle_transition_isolation ON lifecycle_transition
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE lifecycle_transition ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE lifecycle_state ALTER COLUMN organization_id SET NOT NULL;

-- FORCE ROW LEVEL SECURITY is intentionally not reverted per table: dropping it
-- would weaken isolation, and migration 000055 already sets it for the tables it
-- creates. Only the application role is removed.

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM reticora_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE USAGE, SELECT ON SEQUENCES FROM reticora_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE EXECUTE ON FUNCTIONS FROM reticora_app;

REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM reticora_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM reticora_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM reticora_app;
REVOKE USAGE ON SCHEMA public FROM reticora_app;

DROP ROLE IF EXISTS reticora_app;
