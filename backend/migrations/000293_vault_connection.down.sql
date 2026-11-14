-- Reverts 000293: the vault connections, their triggers and functions go,
-- and credential.kind loses vault_approle and vault_token (000149). The
-- revert is refused while a credential of these kinds exists; delete it
-- first. The unique keys on credential and collector stay: migrations
-- 000287/000288 created them and other foreign keys reference them.
DROP TABLE vault_connection_collector;
DROP TABLE vault_connection;
DROP FUNCTION propagate_vault_connection_scope();
DROP FUNCTION derive_vault_connection_collector();
DROP FUNCTION check_vault_connection_auth();

DO $$
BEGIN
    ALTER TABLE credential DROP CONSTRAINT credential_kind_check;
    ALTER TABLE credential ADD CONSTRAINT credential_kind_check
        CHECK (kind IN ('snmp_v2c', 'snmp_v3', 'ssh_password', 'ssh_key', 'redfish', 'ipmi', 'wmi', 'api_token', 'nut', 'nas'));
EXCEPTION WHEN check_violation THEN
    RAISE EXCEPTION 'credential holds credentials of kind vault_approle or vault_token; migration 000293 cannot be reverted'
        USING ERRCODE = 'check_violation';
END
$$;
