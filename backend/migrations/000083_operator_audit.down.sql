-- Migration 000083 down.

DROP TABLE IF EXISTS operator_audit;
DROP FUNCTION IF EXISTS reject_operator_audit_mutation();
