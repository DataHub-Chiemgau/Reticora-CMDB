-- Migration 000043: enrollment code lookup runs pre-tenant
--
-- A collector redeems its enrollment code before it has any tenant context;
-- the code hash is the credential. The single read that resolves the code to
-- its organization runs under the dedicated app.system flag (same pattern as
-- the webhook dispatcher), which no request-scoped code path ever sets. The
-- consume UPDATE still runs under the resolved tenant's RLS context.
DROP POLICY IF EXISTS collector_enrollment_code_isolation ON collector_enrollment_code;
CREATE POLICY collector_enrollment_code_isolation ON collector_enrollment_code
    USING (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    )
    WITH CHECK (
        organization_id = NULLIF(current_setting('app.org_id', true), '')::UUID
        OR current_setting('app.system', true) = 'on'
    );
