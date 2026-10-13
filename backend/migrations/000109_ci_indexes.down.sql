-- Migration 000109 down: the single-column indexes and the default GIN
-- operator class again.

CREATE INDEX idx_ci_location ON ci (location_id) WHERE location_id IS NOT NULL;
CREATE INDEX idx_ci_asset ON ci (asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX idx_ci_parent ON ci (parent_ci_id) WHERE parent_ci_id IS NOT NULL;
CREATE INDEX idx_ci_attributes ON ci USING gin (attributes);

DROP INDEX idx_ci_attributes_path;
DROP INDEX idx_ci_org_parent;
DROP INDEX idx_ci_org_asset;
DROP INDEX idx_ci_org_location;
