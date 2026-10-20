-- Reverts WP-379: credential.kind without nas (000021). The revert is refused
-- while a credential of kind nas exists; delete it first. The check of the
-- new constraint sees every row regardless of row level security.

DO $$
BEGIN
    ALTER TABLE credential DROP CONSTRAINT credential_kind_check;
    ALTER TABLE credential ADD CONSTRAINT credential_kind_check
        CHECK (kind IN ('snmp_v2c', 'snmp_v3', 'ssh_password', 'ssh_key', 'redfish', 'ipmi', 'wmi', 'api_token', 'nut'));
EXCEPTION WHEN check_violation THEN
    RAISE EXCEPTION 'credential holds credentials of kind nas; migration 000149 cannot be reverted'
        USING ERRCODE = 'check_violation';
END
$$;
