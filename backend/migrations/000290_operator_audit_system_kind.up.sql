-- Migration 000290: operator_audit knows the operator kind 'system' (WP-884,
-- ENT-07, E-157). The trial end (system job entitlement.trial_end) records
-- its plan change under operator_kind = 'system' with operator_id
-- entitlement.trial_end. Pattern 000103.
ALTER TABLE operator_audit DROP CONSTRAINT operator_audit_operator_kind_check;
ALTER TABLE operator_audit ADD CONSTRAINT operator_audit_operator_kind_check
    CHECK (operator_kind IN ('oidc', 'break_glass', 'signup', 'system'));
