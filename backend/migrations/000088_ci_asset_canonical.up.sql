-- WP-077 (DB-05, CH12, AST-04): the asset is canonical for serial number,
-- location, physical lifecycle and inventory data of the CIs linked to it.
--
-- * ci.asset_id links a CI to its asset (n CIs per asset; asset.ci_id stays
--   the asset's primary CI and is kept in sync).
-- * ci.location_id mirrors asset.location_id; a direct change of the
--   location of a linked CI is rejected (constraint ci_location_from_asset).
-- * ci.serial_number stays the observed value for identity resolution; a
--   difference to asset.serial_number opens one review item serial_mismatch
--   per CI.

ALTER TABLE ci ADD COLUMN asset_id uuid;
ALTER TABLE ci ADD CONSTRAINT ci_asset_id_fkey FOREIGN KEY (asset_id, organization_id)
    REFERENCES asset (id, organization_id) ON DELETE SET NULL (asset_id);
CREATE INDEX idx_ci_asset ON ci (asset_id) WHERE asset_id IS NOT NULL;

-- Existing links: asset.ci_id. An asset without a location takes the
-- location of its CI first, so the mirror loses no location.
UPDATE asset a SET location_id = c.location_id
FROM ci c
WHERE a.ci_id = c.id AND a.location_id IS NULL AND c.location_id IS NOT NULL;
UPDATE ci c SET asset_id = a.id
FROM asset a
WHERE a.ci_id = c.id AND a.organization_id = c.organization_id;

ALTER TABLE review_item DROP CONSTRAINT review_item_kind_check;
ALTER TABLE review_item ADD CONSTRAINT review_item_kind_check
    CHECK (kind = ANY (ARRAY['ambiguous_identity', 'conflicting_values', 'unclassified_device', 'override_conflict',
                             'unlicensed_ci', 'serial_mismatch']));
CREATE UNIQUE INDEX review_item_open_serial_mismatch
    ON review_item (organization_id, (payload ->> 'ci_id'))
    WHERE kind = 'serial_mismatch' AND status = 'open';

-- The location of a linked CI is the asset's location.
CREATE FUNCTION ci_mirror_asset_location() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    asset_location uuid;
BEGIN
    IF NEW.asset_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT a.location_id INTO asset_location FROM asset a WHERE a.id = NEW.asset_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'asset % not found', NEW.asset_id
            USING ERRCODE = '23503', CONSTRAINT = 'ci_asset_id_fkey';
    END IF;
    -- A direct write (not the mirror cascade from the asset) of another
    -- location on a linked CI is rejected.
    IF TG_OP = 'UPDATE' AND pg_trigger_depth() = 1
       AND NEW.asset_id IS NOT DISTINCT FROM OLD.asset_id
       AND NEW.location_id IS DISTINCT FROM OLD.location_id
       AND NEW.location_id IS DISTINCT FROM asset_location THEN
        RAISE EXCEPTION 'the location of CI % is the location of its asset', NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_location_from_asset';
    END IF;
    NEW.location_id := asset_location;
    RETURN NEW;
END
$$;

-- Fires before trg_ci_derive_location (name order), which derives site and
-- room from the mirrored location; that trigger now also fires on asset_id.
CREATE TRIGGER trg_ci_asset_location BEFORE INSERT OR UPDATE OF location_id, asset_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_mirror_asset_location();
DROP TRIGGER trg_ci_derive_location ON ci;
CREATE TRIGGER trg_ci_derive_location BEFORE INSERT OR UPDATE OF location_id, site_id, room_id, client_id, asset_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_derive_location();

-- A moved asset moves its CIs.
CREATE FUNCTION asset_propagate_location() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ci SET location_id = NEW.location_id
    WHERE asset_id = NEW.id AND location_id IS DISTINCT FROM NEW.location_id;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_asset_propagate_location AFTER UPDATE OF location_id ON asset
    FOR EACH ROW WHEN (OLD.location_id IS DISTINCT FROM NEW.location_id)
    EXECUTE FUNCTION asset_propagate_location();

-- asset.ci_id (the primary CI) links that CI to the asset.
CREATE FUNCTION asset_sync_primary_ci() RETURNS trigger
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
CREATE TRIGGER trg_asset_sync_primary_ci AFTER INSERT OR UPDATE OF ci_id ON asset
    FOR EACH ROW EXECUTE FUNCTION asset_sync_primary_ci();

-- An observed serial number that differs from the asset's opens a review.
CREATE FUNCTION review_serial_mismatch(p_ci_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
    SELECT c.organization_id, 'serial_mismatch', 'open',
           jsonb_build_object('ci_id', c.id, 'asset_id', a.id,
                              'ci_serial_number', c.serial_number, 'asset_serial_number', a.serial_number),
           ARRAY[c.id]
    FROM ci c JOIN asset a ON a.id = c.asset_id
    WHERE c.id = p_ci_id
      AND NULLIF(btrim(c.serial_number), '') IS NOT NULL
      AND NULLIF(btrim(a.serial_number), '') IS NOT NULL
      AND lower(btrim(c.serial_number)) <> lower(btrim(a.serial_number))
    ON CONFLICT (organization_id, (payload ->> 'ci_id')) WHERE kind = 'serial_mismatch' AND status = 'open'
    DO NOTHING;
END
$$;

CREATE FUNCTION ci_check_serial_mismatch() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.asset_id IS NOT NULL THEN
        PERFORM review_serial_mismatch(NEW.id);
    END IF;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_ci_serial_mismatch AFTER INSERT OR UPDATE OF serial_number, asset_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_check_serial_mismatch();

CREATE FUNCTION asset_check_serial_mismatch() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    linked uuid;
BEGIN
    FOR linked IN SELECT id FROM ci WHERE asset_id = NEW.id LOOP
        PERFORM review_serial_mismatch(linked);
    END LOOP;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_asset_serial_mismatch AFTER UPDATE OF serial_number ON asset
    FOR EACH ROW WHEN (OLD.serial_number IS DISTINCT FROM NEW.serial_number)
    EXECUTE FUNCTION asset_check_serial_mismatch();
