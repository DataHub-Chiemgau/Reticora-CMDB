-- Down of 000357: the managed probes go first (the key of WP-935 would fail
-- on their duplicates), then the target table, the probe columns with the
-- restored constraint and the scan columns of subnet. subnet_id_organization_key
-- stays: 000287 created it, and every later reference needs it.

DROP TABLE subnet_scan_target;
DROP FUNCTION subnet_scan_target_scope();
DROP FUNCTION subnet_scan_target_release();
DROP TRIGGER subnet_scan_scope_propagation ON subnet;
DROP FUNCTION propagate_subnet_scan_scope();

DELETE FROM probe WHERE managed_by IS NOT NULL;
DROP INDEX probe_managed_key;
DROP INDEX probe_target_key;
ALTER TABLE probe ADD CONSTRAINT probe_target_key UNIQUE NULLS NOT DISTINCT (organization_id, client_id, ci_id, type, target);
ALTER TABLE probe
    DROP CONSTRAINT probe_managed_pair,
    DROP COLUMN managed_key,
    DROP COLUMN managed_by;

DROP INDEX idx_subnet_collector_group;
ALTER TABLE subnet
    DROP CONSTRAINT subnet_collector_group_fkey,
    DROP CONSTRAINT subnet_scan_collector_group_required,
    DROP COLUMN scan_synced_at,
    DROP COLUMN offline_after_s,
    DROP COLUMN discover_new_hosts,
    DROP COLUMN scan_interval_s,
    DROP COLUMN collector_group_id,
    DROP COLUMN scan_enabled;
