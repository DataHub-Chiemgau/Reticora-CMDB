-- Reverts 000368_collector_group_trap_config.

ALTER TABLE collector_group DROP COLUMN trap_config;
