DROP FUNCTION IF EXISTS asset_has_linked_ci(uuid);

CREATE OR REPLACE FUNCTION asset_version_bump() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(NEW) - 'updated_at' - 'version') IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at' - 'version') THEN
        NEW.version := OLD.version + 1;
    ELSE
        NEW.version := OLD.version;
    END IF;
    RETURN NEW;
END
$$;

DROP INDEX IF EXISTS idx_asset_org_serial_norm;
ALTER TABLE asset DROP COLUMN IF EXISTS serial_number_norm;
