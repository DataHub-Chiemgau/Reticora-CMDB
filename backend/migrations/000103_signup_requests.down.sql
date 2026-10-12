-- Migration 000103 down: drop self-signup requests.
--
-- operator_audit is append-only, so its signup rows cannot be removed; the
-- kind constraint is restored only when none exist.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM operator_audit WHERE operator_kind = 'signup') THEN
        RAISE EXCEPTION 'operator_audit holds signup entries; migration 000103 cannot be reverted';
    END IF;
END
$$;
ALTER TABLE operator_audit DROP CONSTRAINT operator_audit_operator_kind_check;
ALTER TABLE operator_audit ADD CONSTRAINT operator_audit_operator_kind_check
    CHECK (operator_kind IN ('oidc', 'break_glass'));

DROP TABLE signup_request;
