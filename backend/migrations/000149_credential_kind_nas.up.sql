-- WP-379 (DIS-04, COL-02; F-011, E-68 option A): credential kind nas, the
-- account of the NAS plugin (Synology DSM: username and password).
--
-- WP-378 removed the alias nas -> ssh, so the SSH account no longer reaches
-- NAS targets (F-028); without a kind of its own the NAS plugin received no
-- credentials (fail closed). The server accepts the kind in validKinds, the
-- collector files it under the namespace nas (contract
-- collector/collectorcmd/testdata/credential-kinds.json). RLS of credential
-- is unchanged.

ALTER TABLE credential DROP CONSTRAINT credential_kind_check;
ALTER TABLE credential ADD CONSTRAINT credential_kind_check
    CHECK (kind IN ('snmp_v2c', 'snmp_v3', 'ssh_password', 'ssh_key', 'redfish', 'ipmi', 'wmi', 'api_token', 'nut', 'nas'));
