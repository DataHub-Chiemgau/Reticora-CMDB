-- WP-272 (AST-04, AST-03, AST-01): the primary CI of an asset (asset.ci_id,
-- API name primary_ci_id, E-122) stays consistent with ci.asset_id in both
-- directions, and a CI and its asset agree on the client.
--
-- * ci.asset_id is the link (n CIs per asset). asset.ci_id is optional and
--   names one of them.
-- * ci_sync_asset_primary: a live CI that loses its asset or moves to
--   another one clears asset.ci_id of the old asset when it names the CI.
--   When the old asset is visible but may not be changed (a client-scoped
--   principal and an org-wide asset, asset_scoped_update of 000079), the
--   write fails with 42501 instead of leaving the primary CI behind.
--   Deleting, merging, restoring and hard-deleting a CI keep asset.ci_id
--   themselves (they change deleted_at in the same statement or clear
--   asset.ci_id first).
-- * asset_sync_primary_ci only links: the previous primary CI stays linked.
-- * ci_asset_client: a CI and its asset with both a client must have the
--   same one (23514, constraint ci_asset_client); NULL on either side is
--   compatible. Checked on insert and when asset_id or client_id change, as
--   ci_location_client (000074), so existing links and snapshot restores
--   (unmerge) do not fail.
--
-- The triggers run as the caller (no SECURITY DEFINER): row level security
-- applies; an asset the caller cannot see fails in ci_mirror_asset_location
-- with ci_asset_id_fkey as before.

CREATE FUNCTION ci_sync_asset_primary() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE asset SET ci_id = NULL WHERE id = OLD.asset_id AND ci_id = NEW.id;
    IF NOT FOUND AND EXISTS (SELECT 1 FROM asset WHERE id = OLD.asset_id AND ci_id = NEW.id) THEN
        RAISE EXCEPTION 'the primary CI of asset % cannot be cleared in this scope', OLD.asset_id
            USING ERRCODE = '42501';
    END IF;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_ci_sync_asset_primary AFTER UPDATE OF asset_id ON ci
    FOR EACH ROW WHEN (OLD.asset_id IS NOT NULL AND OLD.asset_id IS DISTINCT FROM NEW.asset_id
                       AND NEW.deleted_at IS NULL)
    EXECUTE FUNCTION ci_sync_asset_primary();

-- asset.ci_id (the primary CI) links that CI to the asset; the previous
-- primary CI stays linked (an asset has n CIs).
CREATE OR REPLACE FUNCTION asset_sync_primary_ci() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.ci_id IS NOT NULL THEN
        UPDATE ci SET asset_id = NEW.id WHERE id = NEW.ci_id AND asset_id IS DISTINCT FROM NEW.id;
    END IF;
    RETURN NULL;
END
$$;

CREATE FUNCTION ci_asset_client() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    asset_client uuid;
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        IF NEW.asset_id IS NULL OR NEW.client_id IS NULL
           OR (TG_OP = 'UPDATE' AND NEW.asset_id IS NOT DISTINCT FROM OLD.asset_id
               AND NEW.client_id IS NOT DISTINCT FROM OLD.client_id) THEN
            RETURN NEW;
        END IF;
        SELECT a.client_id INTO asset_client FROM asset a WHERE a.id = NEW.asset_id;
        IF asset_client IS NOT NULL AND asset_client <> NEW.client_id THEN
            RAISE EXCEPTION 'CI % and asset % belong to different clients', NEW.id, NEW.asset_id
                USING ERRCODE = '23514', CONSTRAINT = 'ci_asset_client';
        END IF;
        RETURN NEW;
    END IF;
    -- asset: a changed client must match every linked CI with a client.
    IF NEW.client_id IS NULL OR NEW.client_id IS NOT DISTINCT FROM OLD.client_id THEN
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM ci c WHERE c.asset_id = NEW.id AND c.client_id IS NOT NULL AND c.client_id <> NEW.client_id) THEN
        RAISE EXCEPTION 'asset % has linked CIs of another client', NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_asset_client';
    END IF;
    RETURN NEW;
END
$$;
-- Fires before trg_ci_asset_location (name order), so a visible asset of
-- another client is a client conflict; an invisible one still fails there.
CREATE TRIGGER trg_ci_asset_client BEFORE INSERT OR UPDATE OF asset_id, client_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_asset_client();
CREATE TRIGGER trg_asset_ci_client BEFORE UPDATE OF client_id ON asset
    FOR EACH ROW EXECUTE FUNCTION ci_asset_client();
