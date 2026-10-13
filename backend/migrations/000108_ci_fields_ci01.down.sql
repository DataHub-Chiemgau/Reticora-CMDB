-- Migration 000108 down: no merged_into_id; import falls back to manual.

DROP INDEX idx_ci_merged_into;
ALTER TABLE ci DROP CONSTRAINT ci_merged_into_not_self;
ALTER TABLE ci DROP CONSTRAINT ci_merged_into_fkey;
ALTER TABLE ci DROP COLUMN merged_into_id;

ALTER TABLE ci DROP CONSTRAINT ci_discovery_source_check;
ALTER TABLE ci DISABLE TRIGGER trg_ci_updated_at;
UPDATE ci SET discovery_source = 'manual' WHERE discovery_source = 'import';
ALTER TABLE ci ENABLE TRIGGER trg_ci_updated_at;
ALTER TABLE ci ADD CONSTRAINT ci_discovery_source_check
    CHECK (discovery_source IN ('snmp','ssh','redfish','ipmi','wmi','api','agent','sweep','manual'));
