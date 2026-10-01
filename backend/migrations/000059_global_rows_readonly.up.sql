-- Migration 000059: global catalog rows read-only, organization column for
-- child tables (WP-024, TEN-02, TEN-05, E-10)
--
-- 1. ci_type, lifecycle_definition, lifecycle_state, lifecycle_transition and
--    relationship_type hold global catalog rows (organization_id IS NULL).
--    Their single ALL policy let every tenant change or delete these rows
--    because USING (the row filter of UPDATE and DELETE) accepted them. The
--    policy is split per command: SELECT may read global rows, INSERT, UPDATE
--    and DELETE require organization_id = app.org_id.
-- 2. Tables without organization_id (E-10, classification per table):
--    - permission: global permission catalogue, maintained by migrations only.
--      Read-only for reticora_app (documented exception of the RLS catalog
--      test, rls.GlobalCatalogTables).
--    - ci_type_attribute: attributes of a CI type. Gets a nullable
--      organization_id copied from its ci_type (NULL for global types, which
--      makes their attributes read-only like the type).
--    - team_member, user_custom_role: tenant data. Get a NOT NULL
--      organization_id copied from team or custom_role; team_member also
--      checks the team scope.
--    The column is set by a trigger from the parent row, so writers need no
--    change and cannot choose another organization. Policies check the
--    column against app.org_id.

-- ─── 1. Global catalog rows ────────────────────────────────────────────────────

DO $$
DECLARE
    entry RECORD;
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('ci_type', 'ci_type_isolation'),
            ('lifecycle_definition', 'lifecycle_definition_isolation'),
            ('lifecycle_state', 'lifecycle_state_isolation'),
            ('lifecycle_transition', 'lifecycle_transition_isolation'),
            ('relationship_type', 'relationship_type_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (organization_id IS NULL OR %s)',
            entry.policy_name || '_select', entry.table_name, org_match);
        EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (%s)',
            entry.policy_name || '_insert', entry.table_name, org_match);
        EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (%s) WITH CHECK (%s)',
            entry.policy_name || '_update', entry.table_name, org_match, org_match);
        EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (%s)',
            entry.policy_name || '_delete', entry.table_name, org_match);
    END LOOP;
END
$$;

-- ─── 2a. permission: global catalogue, read-only for the application ─────────

REVOKE INSERT, UPDATE, DELETE ON permission FROM reticora_app;

-- ─── 2b. ci_type_attribute ─────────────────────────────────────────────────────

ALTER TABLE ci_type_attribute
    ADD COLUMN organization_id UUID REFERENCES organization(id) ON DELETE CASCADE;
UPDATE ci_type_attribute a SET organization_id = ct.organization_id
    FROM ci_type ct WHERE ct.id = a.ci_type_id;
CREATE INDEX idx_ci_type_attribute_org ON ci_type_attribute(organization_id);

CREATE FUNCTION ci_type_attribute_set_org() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT ct.organization_id INTO NEW.organization_id FROM ci_type ct WHERE ct.id = NEW.ci_type_id;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ci_type_attribute_set_org
    BEFORE INSERT OR UPDATE OF ci_type_id, organization_id ON ci_type_attribute
    FOR EACH ROW EXECUTE FUNCTION ci_type_attribute_set_org();

DROP POLICY IF EXISTS ci_type_attribute_isolation ON ci_type_attribute;
CREATE POLICY ci_type_attribute_isolation_select ON ci_type_attribute FOR SELECT
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY ci_type_attribute_isolation_insert ON ci_type_attribute FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY ci_type_attribute_isolation_update ON ci_type_attribute FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
CREATE POLICY ci_type_attribute_isolation_delete ON ci_type_attribute FOR DELETE
    USING (organization_id = current_setting('app.org_id')::UUID);

-- ─── 2c. team_member ───────────────────────────────────────────────────────────

ALTER TABLE team_member
    ADD COLUMN organization_id UUID REFERENCES organization(id) ON DELETE CASCADE;
UPDATE team_member m SET organization_id = t.organization_id
    FROM team t WHERE t.id = m.team_id;
ALTER TABLE team_member ALTER COLUMN organization_id SET NOT NULL;
CREATE INDEX idx_team_member_org ON team_member(organization_id);

CREATE FUNCTION team_member_set_org() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT t.organization_id INTO NEW.organization_id FROM team t WHERE t.id = NEW.team_id;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_team_member_set_org
    BEFORE INSERT OR UPDATE OF team_id, organization_id ON team_member
    FOR EACH ROW EXECUTE FUNCTION team_member_set_org();

-- With its own organization_id the table is a tenant table with team_id, so
-- the team scope applies as for every table with team_id (CH25, E-11):
-- memberships of teams outside app.team_scope are invisible and not writable.
-- An empty app.team_scope means all teams of the organization.
DROP POLICY IF EXISTS team_member_isolation ON team_member;
CREATE POLICY team_member_isolation ON team_member
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.team_scope', true), '') IS NULL
             OR team_id = ANY (string_to_array(current_setting('app.team_scope', true), ',')::uuid[]))
    );

-- ─── 2d. user_custom_role ──────────────────────────────────────────────────────

ALTER TABLE user_custom_role
    ADD COLUMN organization_id UUID REFERENCES organization(id) ON DELETE CASCADE;
UPDATE user_custom_role u SET organization_id = cr.organization_id
    FROM custom_role cr WHERE cr.id = u.custom_role_id;
ALTER TABLE user_custom_role ALTER COLUMN organization_id SET NOT NULL;
CREATE INDEX idx_user_custom_role_org ON user_custom_role(organization_id);

CREATE FUNCTION user_custom_role_set_org() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT cr.organization_id INTO NEW.organization_id FROM custom_role cr WHERE cr.id = NEW.custom_role_id;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_user_custom_role_set_org
    BEFORE INSERT OR UPDATE OF custom_role_id, organization_id ON user_custom_role
    FOR EACH ROW EXECUTE FUNCTION user_custom_role_set_org();

DROP POLICY IF EXISTS user_custom_role_isolation ON user_custom_role;
CREATE POLICY user_custom_role_isolation ON user_custom_role
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
