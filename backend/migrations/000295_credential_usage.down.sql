-- Reverts 000295: the usage rows, their triggers and functions go.
DROP TRIGGER IF EXISTS trg_ci_credential_usage_scope ON ci;
DROP TABLE credential_usage;
DROP FUNCTION propagate_credential_usage_scope();
DROP FUNCTION derive_credential_usage_scope();
