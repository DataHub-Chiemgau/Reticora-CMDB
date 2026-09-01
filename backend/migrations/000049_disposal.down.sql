DROP TRIGGER IF EXISTS disposal_record_no_update ON disposal_record;
DROP FUNCTION IF EXISTS disposal_record_immutable();
DROP TABLE IF EXISTS disposal_record;
DELETE FROM permission WHERE key IN ('disposal:read','disposal:write');
