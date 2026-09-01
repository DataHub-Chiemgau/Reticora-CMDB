-- Migration 000041: close RLS gaps on tenant-scoped tables
--
-- Audit finding: these tables carried organization-linked data but had no RLS
-- policy / FORCE ROW LEVEL SECURITY, so a non-privileged application role
-- (which production should use; the compose superuser bypasses RLS entirely)
-- would either see every tenant's rows or none. Policies follow the existing
-- app.org_id session-variable pattern; tables without their own
-- organization_id column scope through their parent row.

-- metric_sample is intentionally NOT covered here: it is a TimescaleDB
-- hypertable with columnstore, and PostgreSQL/TimescaleDB does not support
-- row-level security on it (migration 000020 documents this). Tenant isolation
-- for metrics is enforced at the repository layer (every query carries
-- organization_id); this exception is pinned by test.

-- org_dek: tenant key material, direct organization_id. This table must never
-- be readable across tenants.
ALTER TABLE org_dek ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_dek FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS org_dek_isolation ON org_dek;
CREATE POLICY org_dek_isolation ON org_dek
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- ci_type_attribute: scoped through the parent ci_type (which also exposes
-- global system types as organization_id NULL).
ALTER TABLE ci_type_attribute ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_type_attribute FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ci_type_attribute_isolation ON ci_type_attribute;
CREATE POLICY ci_type_attribute_isolation ON ci_type_attribute
    USING (EXISTS (
        SELECT 1 FROM ci_type ct
        WHERE ct.id = ci_type_attribute.ci_type_id
          AND (ct.organization_id IS NULL OR ct.organization_id = current_setting('app.org_id')::UUID)
    ))
    WITH CHECK (EXISTS (
        SELECT 1 FROM ci_type ct
        WHERE ct.id = ci_type_attribute.ci_type_id
          AND ct.organization_id = current_setting('app.org_id')::UUID
    ));

-- team_member: scoped through the parent team.
ALTER TABLE team_member ENABLE ROW LEVEL SECURITY;
ALTER TABLE team_member FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS team_member_isolation ON team_member;
CREATE POLICY team_member_isolation ON team_member
    USING (EXISTS (
        SELECT 1 FROM team t
        WHERE t.id = team_member.team_id
          AND t.organization_id = current_setting('app.org_id')::UUID
    ))
    WITH CHECK (EXISTS (
        SELECT 1 FROM team t
        WHERE t.id = team_member.team_id
          AND t.organization_id = current_setting('app.org_id')::UUID
    ));

-- user_custom_role: scoped through the parent custom_role.
ALTER TABLE user_custom_role ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_custom_role FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS user_custom_role_isolation ON user_custom_role;
CREATE POLICY user_custom_role_isolation ON user_custom_role
    USING (EXISTS (
        SELECT 1 FROM custom_role cr
        WHERE cr.id = user_custom_role.custom_role_id
          AND cr.organization_id = current_setting('app.org_id')::UUID
    ))
    WITH CHECK (EXISTS (
        SELECT 1 FROM custom_role cr
        WHERE cr.id = user_custom_role.custom_role_id
          AND cr.organization_id = current_setting('app.org_id')::UUID
    ));
