-- Reverts 000290. operator_audit is append-only (hash chain): rows of the
-- kind 'system' are never deleted, so the down migration stops while one
-- exists instead of breaking the chain.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM operator_audit WHERE operator_kind = 'system') THEN
        RAISE EXCEPTION 'operator_audit holds entries of the operator kind system (trial end, WP-884); migration 000290 cannot be reverted without breaking the audit chain';
    END IF;
END
$$;
ALTER TABLE operator_audit DROP CONSTRAINT operator_audit_operator_kind_check;
ALTER TABLE operator_audit ADD CONSTRAINT operator_audit_operator_kind_check
    CHECK (operator_kind IN ('oidc', 'break_glass', 'signup'));
