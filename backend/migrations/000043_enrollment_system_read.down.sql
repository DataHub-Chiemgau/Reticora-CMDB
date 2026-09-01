DROP POLICY IF EXISTS collector_enrollment_code_isolation ON collector_enrollment_code;
CREATE POLICY collector_enrollment_code_isolation ON collector_enrollment_code
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
