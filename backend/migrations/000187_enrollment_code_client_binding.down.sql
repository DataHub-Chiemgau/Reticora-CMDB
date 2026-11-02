DROP POLICY IF EXISTS collector_enrollment_code_scope_select ON collector_enrollment_code;
DROP POLICY IF EXISTS collector_enrollment_code_scope_insert ON collector_enrollment_code;
DROP POLICY IF EXISTS collector_enrollment_code_scope_update ON collector_enrollment_code;
DROP POLICY IF EXISTS collector_enrollment_code_scope_delete ON collector_enrollment_code;
DROP TRIGGER IF EXISTS trg_collector_enrollment_code_site_binding ON collector_enrollment_code;
DROP FUNCTION IF EXISTS collector_enrollment_code_site_binding();
ALTER TABLE collector_enrollment_code
    DROP CONSTRAINT IF EXISTS collector_enrollment_code_binding_check,
    DROP CONSTRAINT IF EXISTS collector_enrollment_code_site_fkey,
    DROP CONSTRAINT IF EXISTS collector_enrollment_code_client_fkey,
    DROP COLUMN IF EXISTS site_id,
    DROP COLUMN IF EXISTS client_id,
    DROP COLUMN IF EXISTS binding_recorded;
