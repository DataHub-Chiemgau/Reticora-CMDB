-- Rollback migration 000021

DROP TABLE IF EXISTS review_item;
DROP TABLE IF EXISTS org_dek;
DROP TABLE IF EXISTS credential;
DROP TABLE IF EXISTS relationship_suppression;
DROP TABLE IF EXISTS rack_mount;

-- Global system types (organization_id NULL) only exist since this migration;
-- remove them so organization_id can be NOT NULL again. CIs of these types
-- block the rollback through their foreign key instead of losing data.
DELETE FROM ci_type WHERE organization_id IS NULL;
ALTER TABLE ci_type ALTER COLUMN organization_id SET NOT NULL;

ALTER TABLE ci_type DROP COLUMN IF EXISTS key;
ALTER TABLE ci_type DROP COLUMN IF EXISTS display_name;
ALTER TABLE ci_type DROP COLUMN IF EXISTS is_system;
ALTER TABLE ci_type DROP COLUMN IF EXISTS attribute_schema;
ALTER TABLE ci_type DROP COLUMN IF EXISTS required_fields;

ALTER TABLE building DROP COLUMN IF EXISTS floorplan_object_key;

ALTER TABLE site DROP CONSTRAINT IF EXISTS site_org_name_unique;
ALTER TABLE site DROP COLUMN IF EXISTS geo_lat;
ALTER TABLE site DROP COLUMN IF EXISTS geo_lon;
ALTER TABLE site DROP COLUMN IF EXISTS notes;

ALTER TABLE rack DROP COLUMN IF EXISTS width_mm;
ALTER TABLE rack DROP COLUMN IF EXISTS depth_mm;
ALTER TABLE rack DROP COLUMN IF EXISTS notes;
ALTER TABLE rack DROP CONSTRAINT IF EXISTS rack_height_u_check;

DROP INDEX IF EXISTS idx_ci_fts;
DROP INDEX IF EXISTS idx_ci_primary_mac;
DROP INDEX IF EXISTS idx_ci_hardware_uuid;
DROP INDEX IF EXISTS idx_ci_org_status;
DROP INDEX IF EXISTS idx_ci_org_type;

ALTER TABLE ci DROP CONSTRAINT IF EXISTS ci_status_check;
ALTER TABLE ci ADD CONSTRAINT ci_status_check
    CHECK (status IN ('active','inactive','maintenance','decommissioned'));

ALTER TABLE ci DROP COLUMN IF EXISTS site_id;
ALTER TABLE ci DROP COLUMN IF EXISTS room_id;
ALTER TABLE ci DROP COLUMN IF EXISTS hardware_uuid;
ALTER TABLE ci DROP COLUMN IF EXISTS primary_mac;
ALTER TABLE ci DROP COLUMN IF EXISTS hostname;
ALTER TABLE ci DROP COLUMN IF EXISTS fqdn;
ALTER TABLE ci DROP COLUMN IF EXISTS os_name;
ALTER TABLE ci DROP COLUMN IF EXISTS os_version;
ALTER TABLE ci DROP COLUMN IF EXISTS sys_object_id;
ALTER TABLE ci DROP COLUMN IF EXISTS discovery_source;
ALTER TABLE ci DROP COLUMN IF EXISTS first_seen_at;
ALTER TABLE ci DROP COLUMN IF EXISTS last_seen_at;
ALTER TABLE ci DROP COLUMN IF EXISTS is_manual;
ALTER TABLE ci DROP COLUMN IF EXISTS deleted_at;
