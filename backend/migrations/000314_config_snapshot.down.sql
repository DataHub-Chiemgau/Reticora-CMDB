-- Reverts 000314: the configuration versions with their scope triggers.

DROP TRIGGER IF EXISTS config_snapshot_scope_propagation ON ci;
DROP TABLE config_snapshot;

DROP FUNCTION propagate_config_snapshot_scope();
DROP FUNCTION config_snapshot_scope();
