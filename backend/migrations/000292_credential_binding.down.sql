-- Reverts 000292: the bindings, their triggers and functions go. The unique
-- keys on credential and subnet stay: migrations 000287/000288 created them
-- and other foreign keys reference them.
DROP TRIGGER IF EXISTS trg_ci_credential_binding_scope ON ci;
DROP TRIGGER IF EXISTS trg_subnet_credential_binding_scope ON subnet;
DROP TABLE credential_binding;
DROP FUNCTION propagate_credential_binding_scope();
DROP FUNCTION derive_credential_binding_scope();
