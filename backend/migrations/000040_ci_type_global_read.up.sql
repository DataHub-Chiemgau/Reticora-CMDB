-- Migration 000040: global (system) CI types readable by every tenant
--
-- Migration 000021 made ci_type.organization_id nullable and seeded the
-- system types with organization_id NULL, but the RLS policy only matched
-- rows for the current tenant. A non-privileged application role could
-- therefore not resolve type names like "server" during discovery ingest.
-- The policy now also exposes the global system types to every tenant.
DROP POLICY IF EXISTS ci_type_isolation ON ci_type;
CREATE POLICY ci_type_isolation ON ci_type
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
