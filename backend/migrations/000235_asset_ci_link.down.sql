DROP TRIGGER trg_asset_ci_client ON asset;
DROP TRIGGER trg_ci_asset_client ON ci;
DROP FUNCTION ci_asset_client();
DROP TRIGGER trg_ci_sync_asset_primary ON ci;
DROP FUNCTION ci_sync_asset_primary();

-- asset_sync_primary_ci of 000088: changing the primary CI unlinks the
-- previous one.
CREATE OR REPLACE FUNCTION asset_sync_primary_ci() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.ci_id IS NOT NULL AND OLD.ci_id IS DISTINCT FROM NEW.ci_id THEN
        UPDATE ci SET asset_id = NULL WHERE id = OLD.ci_id AND asset_id = OLD.id;
    END IF;
    IF NEW.ci_id IS NOT NULL THEN
        UPDATE ci SET asset_id = NEW.id WHERE id = NEW.ci_id AND asset_id IS DISTINCT FROM NEW.id;
    END IF;
    RETURN NULL;
END
$$;
