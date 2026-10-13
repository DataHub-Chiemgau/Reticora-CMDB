-- Migration 000109: the CI indexes of CI-02 (WP-121).
--
-- (org, location_id), (org, asset_id) and (org, parent_ci_id) replace the
-- single-column indexes; attributes get a GIN index with jsonb_path_ops
-- (containment @>, smaller than the default operator class). (org, type),
-- (org, status), the partial indexes on serial_number, hardware_uuid and
-- management_ip and the simple full-text index exist already.
--
-- golang-migrate runs a file in one transaction, so CREATE INDEX
-- CONCURRENTLY is not possible here. IF NOT EXISTS lets an operator build
-- the indexes beforehand without long locks (docs/schema-baseline.md,
-- "Indizes ohne lange Sperren"); the migration then only drops the
-- replaced ones.

CREATE INDEX IF NOT EXISTS idx_ci_org_location ON ci (organization_id, location_id) WHERE location_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ci_org_asset ON ci (organization_id, asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ci_org_parent ON ci (organization_id, parent_ci_id) WHERE parent_ci_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ci_attributes_path ON ci USING gin (attributes jsonb_path_ops);

DROP INDEX IF EXISTS idx_ci_location;
DROP INDEX IF EXISTS idx_ci_asset;
DROP INDEX IF EXISTS idx_ci_parent;
DROP INDEX IF EXISTS idx_ci_attributes;
