DROP INDEX IF EXISTS idx_entity_change_version;
ALTER TABLE entity_change DROP COLUMN entity_version;
DROP TRIGGER asset_version_bump ON asset;
DROP FUNCTION asset_version_bump();
ALTER TABLE asset DROP COLUMN version;
