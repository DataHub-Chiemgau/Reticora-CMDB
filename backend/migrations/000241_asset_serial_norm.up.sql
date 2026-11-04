-- WP-741 (AST-03, AST-04): the ingest links a discovered CI to the asset
-- that carries its serial number (asset.PGRepository.SerialMatches).
--
-- * asset.serial_number_norm is the normal form of the serial number,
--   lower(btrim()) as review_serial_mismatch (000088) compares it, NULL for
--   an empty one. lower() is not leakproof: an index on the expression would
--   stay unused under the row level policies of the application role
--   (000134, K22). Like ci.serial_number_lc (000180) the normal form is a
--   stored generated column compared with the leakproof text equality.
-- * idx_asset_org_serial_norm serves the lookup per organization. On large
--   asset tables it can be built beforehand with CREATE INDEX CONCURRENTLY
--   (docs/schema-baseline.md); the column must exist by then.
-- * asset_version_bump (000236) compares the whole row. In a BEFORE trigger
--   a stored generated column is not computed yet (NULL in NEW), so every
--   update of an asset with a serial number would raise the version; the
--   comparison leaves the normal form out, it follows serial_number.
-- * asset_has_linked_ci(asset) reports whether a live CI is linked to the
--   asset. The lookup of the ingest locks its assets first (FOR UPDATE OF
--   asset) and then asks this VOLATILE function, which reads with a fresh
--   snapshot (READ COMMITTED): a link committed while the lookup waited for
--   the asset row (POST /api/v1/assets/{id}/cis holds its key share through
--   ci_asset_id_fkey) counts. It runs as the caller (no SECURITY DEFINER),
--   row level security applies.
--
-- Row level security is unchanged.

ALTER TABLE asset ADD COLUMN IF NOT EXISTS serial_number_norm TEXT
    GENERATED ALWAYS AS (NULLIF(lower(btrim(serial_number)), '')) STORED;
CREATE INDEX IF NOT EXISTS idx_asset_org_serial_norm ON asset (organization_id, serial_number_norm)
    WHERE serial_number_norm IS NOT NULL;

CREATE OR REPLACE FUNCTION asset_version_bump() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(NEW) - 'updated_at' - 'version' - 'serial_number_norm')
       IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at' - 'version' - 'serial_number_norm') THEN
        NEW.version := OLD.version + 1;
    ELSE
        NEW.version := OLD.version;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION asset_has_linked_ci(p_asset uuid) RETURNS boolean
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    RETURN EXISTS (SELECT 1 FROM ci WHERE ci.asset_id = p_asset AND ci.deleted_at IS NULL);
END
$$;
