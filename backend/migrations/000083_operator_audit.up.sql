-- Migration 000083: organization-spanning operator audit (SEC-07, WP-070).
--
-- Every action on /admin/* is recorded here with a hash chain of its own,
-- separate from the per-organization audit_log. The table has no
-- organization_id: an operator action may concern several or no
-- organizations (target_org names one when it does). Row level security binds
-- it to the operator context (app.operator, database.WithOperator); tenant
-- transactions see and write nothing. Rows are append-only.

CREATE TABLE operator_audit (
    id            BIGSERIAL PRIMARY KEY,
    timestamp     TIMESTAMPTZ NOT NULL,
    operator_id   TEXT NOT NULL,
    operator_kind TEXT NOT NULL CHECK (operator_kind IN ('oidc', 'break_glass')),
    action        TEXT NOT NULL,
    target_org    UUID,
    status        INTEGER NOT NULL,
    details       JSONB NOT NULL DEFAULT '{}',
    previous_hash TEXT NOT NULL,
    entry_hash    TEXT NOT NULL UNIQUE
);
CREATE INDEX idx_operator_audit_timestamp ON operator_audit (timestamp);

ALTER TABLE operator_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE operator_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY operator_audit_operator ON operator_audit
    USING (current_setting('app.operator', true) = 'on')
    WITH CHECK (current_setting('app.operator', true) = 'on');

REVOKE UPDATE, DELETE ON operator_audit FROM reticora_app;

CREATE FUNCTION reject_operator_audit_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'operator_audit rows are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_operator_audit_no_update BEFORE UPDATE ON operator_audit
    FOR EACH ROW EXECUTE FUNCTION reject_operator_audit_mutation();
CREATE TRIGGER trg_operator_audit_no_delete BEFORE DELETE ON operator_audit
    FOR EACH ROW EXECUTE FUNCTION reject_operator_audit_mutation();
