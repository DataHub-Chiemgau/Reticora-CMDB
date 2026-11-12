-- Migration 000312: credential kinds of the probes (PRT-22, COL-02; WP-967).
-- http (username/password or token), gnmi (username/password and/or
-- client_cert/client_key, optional ca) and acs (username/password or token,
-- TR-069/USP) join the kinds of 000149 and the vault logins of 000293
-- (pattern 000149). Each kind has a namespace of its own without alias, so
-- the namespaces of the bindings (000292, CRD-01 to CRD-04) and of the usage
-- rows (000295, CRD-06) are extended likewise. Row level security and the
-- policies of the three tables are unchanged; the service checks the
-- members of the secret.
ALTER TABLE credential DROP CONSTRAINT credential_kind_check;
ALTER TABLE credential ADD CONSTRAINT credential_kind_check
    CHECK (kind IN ('snmp_v2c', 'snmp_v3', 'ssh_password', 'ssh_key', 'redfish', 'ipmi', 'wmi', 'api_token', 'nut', 'nas',
                    'vault_approle', 'vault_token', 'http', 'gnmi', 'acs'));

ALTER TABLE credential_binding DROP CONSTRAINT credential_binding_namespace_check;
ALTER TABLE credential_binding ADD CONSTRAINT credential_binding_namespace_check
    CHECK (namespace IN ('snmp', 'ssh', 'redfish', 'ipmi', 'wmi', 'api', 'nut', 'nas', 'http', 'gnmi', 'acs'));

ALTER TABLE credential_usage DROP CONSTRAINT credential_usage_namespace_check;
ALTER TABLE credential_usage ADD CONSTRAINT credential_usage_namespace_check
    CHECK (namespace IN ('snmp', 'ssh', 'redfish', 'ipmi', 'wmi', 'api', 'nut', 'nas', 'http', 'gnmi', 'acs'));
