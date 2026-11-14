-- Reverts 000367_collector_probe_capabilities.

ALTER TABLE collector DROP COLUMN probe_capabilities;
