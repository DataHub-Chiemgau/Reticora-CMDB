-- Migration 000108: the CI fields of CI-01 (WP-120).
--
-- discovery_source knows import (bulk import by a user); merged_into_id
-- points from a merged CI to the CI it was merged into (written by the
-- merge, CI-12). CIs created manually are manual: the create path left
-- is_manual false, which is corrected for the rows it wrote.

ALTER TABLE ci DROP CONSTRAINT ci_discovery_source_check;
ALTER TABLE ci ADD CONSTRAINT ci_discovery_source_check
    CHECK (discovery_source IN ('snmp','ssh','redfish','ipmi','wmi','api','agent','sweep','manual','import'));

ALTER TABLE ci ADD COLUMN merged_into_id uuid;
ALTER TABLE ci ADD CONSTRAINT ci_merged_into_fkey FOREIGN KEY (merged_into_id, organization_id)
    REFERENCES ci (id, organization_id) ON DELETE SET NULL (merged_into_id);
ALTER TABLE ci ADD CONSTRAINT ci_merged_into_not_self CHECK (merged_into_id IS NULL OR merged_into_id <> id);
CREATE INDEX idx_ci_merged_into ON ci (merged_into_id) WHERE merged_into_id IS NOT NULL;

ALTER TABLE ci DISABLE TRIGGER trg_ci_updated_at;
UPDATE ci SET is_manual = true WHERE discovery_source = 'manual' AND NOT is_manual;
ALTER TABLE ci ENABLE TRIGGER trg_ci_updated_at;
