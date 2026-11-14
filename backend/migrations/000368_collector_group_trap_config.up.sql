-- Migration 000368 (number provisional): the SNMP trap receiver of a
-- collector group (DIS-06; WP-977).
--
-- collector_group.trap_config configures the trap receiver of the group's
-- members: {"enabled": bool, "port": 1-65535 (default 162),
-- "credential_ids": [at most 16 credentials]}. Credentials of kind snmp_v2c
-- give the communities, those of kind snmp_v3 the USM users (evaluated from
-- WP-978). The field rules (port range, the credentials belong to the
-- organization, are org-wide or of the group's client and of one of the two
-- kinds) need table reads and are checked by the handler; the column only
-- requires an object. The assignment set (probewire.TrapConfig) carries it
-- to the collectors, the credentials reach them in their bundle. The
-- default (without port) marks a group never configured: its set carries
-- no trap_config and its collectors keep RETICORA_SNMP_TRAP_LISTEN (AK5);
-- a PATCH stores the whole configuration with its port. RLS,
-- policies and the truncate guard of collector_group stay as they are.

ALTER TABLE collector_group ADD COLUMN trap_config jsonb NOT NULL DEFAULT '{"enabled": false}'
    CONSTRAINT collector_group_trap_config_check CHECK (jsonb_typeof(trap_config) = 'object');
COMMENT ON COLUMN collector_group.trap_config IS
    'SNMP trap receiver of the members (DIS-06): enabled, port, credential_ids (snmp_v2c, snmp_v3)';
