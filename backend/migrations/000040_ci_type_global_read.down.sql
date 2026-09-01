DROP POLICY IF EXISTS ci_type_isolation ON ci_type;
CREATE POLICY ci_type_isolation ON ci_type
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
