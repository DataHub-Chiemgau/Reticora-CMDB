-- Revert migration 000059: restore the ALL policies of the global catalog
-- tables and the parent-based policies of the child tables, drop the
-- organization columns and give reticora_app write access to permission.

DROP POLICY IF EXISTS user_custom_role_isolation ON user_custom_role;
DROP TRIGGER IF EXISTS trg_user_custom_role_set_org ON user_custom_role;
DROP FUNCTION IF EXISTS user_custom_role_set_org();
DROP INDEX IF EXISTS idx_user_custom_role_org;
ALTER TABLE user_custom_role DROP COLUMN organization_id;
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

DROP POLICY IF EXISTS team_member_isolation ON team_member;
DROP TRIGGER IF EXISTS trg_team_member_set_org ON team_member;
DROP FUNCTION IF EXISTS team_member_set_org();
DROP INDEX IF EXISTS idx_team_member_org;
ALTER TABLE team_member DROP COLUMN organization_id;
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

DROP POLICY IF EXISTS ci_type_attribute_isolation_select ON ci_type_attribute;
DROP POLICY IF EXISTS ci_type_attribute_isolation_insert ON ci_type_attribute;
DROP POLICY IF EXISTS ci_type_attribute_isolation_update ON ci_type_attribute;
DROP POLICY IF EXISTS ci_type_attribute_isolation_delete ON ci_type_attribute;
DROP TRIGGER IF EXISTS trg_ci_type_attribute_set_org ON ci_type_attribute;
DROP FUNCTION IF EXISTS ci_type_attribute_set_org();
DROP INDEX IF EXISTS idx_ci_type_attribute_org;
ALTER TABLE ci_type_attribute DROP COLUMN organization_id;
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

GRANT INSERT, UPDATE, DELETE ON permission TO reticora_app;

DO $$
DECLARE
    entry RECORD;
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
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name || '_select', entry.table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name || '_insert', entry.table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name || '_update', entry.table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name || '_delete', entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I '
            'USING (organization_id IS NULL OR organization_id = current_setting(''app.org_id'')::UUID) '
            'WITH CHECK (organization_id = current_setting(''app.org_id'')::UUID)',
            entry.policy_name, entry.table_name
        );
    END LOOP;
END
$$;
