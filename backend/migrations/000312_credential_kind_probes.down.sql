-- Reverts 000312: credential.kind without http, gnmi and acs (000293), the
-- namespaces of credential_binding (000292) and credential_usage (000295)
-- without them. The revert is refused while a credential of these kinds, a
-- binding or a usage row of these namespaces exists; delete it first. The
-- check of the new constraints sees every row regardless of row level
-- security.
DO $$
BEGIN
    ALTER TABLE credential_usage DROP CONSTRAINT credential_usage_namespace_check;
    ALTER TABLE credential_usage ADD CONSTRAINT credential_usage_namespace_check
        CHECK (namespace IN ('snmp', 'ssh', 'redfish', 'ipmi', 'wmi', 'api', 'nut', 'nas'));
EXCEPTION WHEN check_violation THEN
    RAISE EXCEPTION 'credential_usage holds rows of namespace http, gnmi or acs; migration 000312 cannot be reverted'
        USING ERRCODE = 'check_violation';
END
$$;

DO $$
BEGIN
    ALTER TABLE credential_binding DROP CONSTRAINT credential_binding_namespace_check;
    ALTER TABLE credential_binding ADD CONSTRAINT credential_binding_namespace_check
        CHECK (namespace IN ('snmp', 'ssh', 'redfish', 'ipmi', 'wmi', 'api', 'nut', 'nas'));
EXCEPTION WHEN check_violation THEN
    RAISE EXCEPTION 'credential_binding holds bindings of namespace http, gnmi or acs; migration 000312 cannot be reverted'
        USING ERRCODE = 'check_violation';
END
$$;

DO $$
BEGIN
    ALTER TABLE credential DROP CONSTRAINT credential_kind_check;
    ALTER TABLE credential ADD CONSTRAINT credential_kind_check
        CHECK (kind IN ('snmp_v2c', 'snmp_v3', 'ssh_password', 'ssh_key', 'redfish', 'ipmi', 'wmi', 'api_token', 'nut', 'nas',
                        'vault_approle', 'vault_token'));
EXCEPTION WHEN check_violation THEN
    RAISE EXCEPTION 'credential holds credentials of kind http, gnmi or acs; migration 000312 cannot be reverted'
        USING ERRCODE = 'check_violation';
END
$$;
