-- WP-273 (AST-05, AST-04, DB-05; E-123): the child contract of the physical
-- composition on ci.parent_ci_id (000089).
--
-- * ci.child_mode is configuration or standalone and set only for a CI with
--   a parent (ci_child_mode_check, ci_child_mode_parent). The trigger
--   ci_child_mode_default sets standalone when a parent is set without a
--   mode (the compatible default for G1 clients, API-01, E-123 (a)) and
--   clears the mode when the parent goes, also through the
--   ON DELETE SET NULL (parent_ci_id) of a hard delete. Existing children
--   become standalone, their behaviour stays.
-- * A configuration child has no asset of its own
--   (ci_configuration_child_asset): its asset data come from the nearest
--   ancestor with an asset (read by the application), its location from its
--   parent. ci_configuration_child sets the location of a configuration
--   child to the parent's on insert and on every change of parent, mode or
--   location, refuses a direct write of another location
--   (ci_location_from_parent, like ci_location_from_asset of 000088) and
--   requires the client of the parent (ci_configuration_child_client); a
--   parent whose client changes away from that of its configuration
--   children is refused (ci_configuration_parent_client).
-- * ci_propagate_configuration_location carries a new location of a CI to
--   its configuration children, and through their own trigger to their
--   configuration descendants, under the row level security of the writer
--   (asset_propagate_location, 000181): it refuses the change with 42501
--   (constraint ci_configuration_location_scope) when a configuration
--   child outside the scope keeps another location; the check widens
--   client, site and team scope. No SECURITY DEFINER; search_path is fixed.
-- * trg_ci_derive_location also fires on parent_ci_id and child_mode: the
--   location the configuration trigger sets derives site and room (an
--   UPDATE OF trigger fires on the SET list, not on what a BEFORE trigger
--   changes).

ALTER TABLE ci ADD COLUMN child_mode text;

UPDATE ci SET child_mode = 'standalone' WHERE parent_ci_id IS NOT NULL;

ALTER TABLE ci
    ADD CONSTRAINT ci_child_mode_check CHECK (child_mode IN ('configuration', 'standalone')),
    ADD CONSTRAINT ci_child_mode_parent CHECK (child_mode IS NULL OR parent_ci_id IS NOT NULL),
    ADD CONSTRAINT ci_configuration_child_asset CHECK (child_mode IS DISTINCT FROM 'configuration' OR asset_id IS NULL);

-- Default standalone with a parent, no mode without one.
CREATE FUNCTION ci_child_mode_default() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF NEW.parent_ci_id IS NULL THEN
        IF TG_OP = 'UPDATE' AND OLD.parent_ci_id IS NOT NULL THEN
            NEW.child_mode := NULL;
        END IF;
    ELSIF NEW.child_mode IS NULL THEN
        NEW.child_mode := 'standalone';
    END IF;
    RETURN NEW;
END
$$;

-- Fires before trg_ci_configuration_child and trg_ci_derive_location (name
-- order).
CREATE TRIGGER trg_ci_child_mode BEFORE INSERT OR UPDATE OF parent_ci_id, child_mode ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_child_mode_default();

-- The location and the client of a configuration child are its parent's.
CREATE FUNCTION ci_configuration_child() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    parent_location uuid;
    parent_client uuid;
BEGIN
    IF NEW.child_mode IS DISTINCT FROM 'configuration' THEN
        IF TG_OP = 'UPDATE' AND NEW.client_id IS DISTINCT FROM OLD.client_id
           AND EXISTS (SELECT 1 FROM ci c
                        WHERE c.parent_ci_id = NEW.id AND c.organization_id = NEW.organization_id
                          AND c.child_mode = 'configuration' AND c.deleted_at IS NULL
                          AND c.client_id IS DISTINCT FROM NEW.client_id) THEN
            RAISE EXCEPTION 'CI % has configuration children of another client', NEW.id
                USING ERRCODE = '23514', CONSTRAINT = 'ci_configuration_parent_client';
        END IF;
        RETURN NEW;
    END IF;
    SELECT p.location_id, p.client_id INTO parent_location, parent_client
    FROM ci p WHERE p.id = NEW.parent_ci_id AND p.organization_id = NEW.organization_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'parent CI % not found', NEW.parent_ci_id
            USING ERRCODE = '23503', CONSTRAINT = 'ci_parent_ci_fkey';
    END IF;
    IF NEW.client_id IS DISTINCT FROM parent_client THEN
        RAISE EXCEPTION 'configuration child % and its parent % belong to different clients', NEW.id, NEW.parent_ci_id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_configuration_child_client';
    END IF;
    -- A direct write (not the propagation from the parent) of another
    -- location on a configuration child is refused.
    IF TG_OP = 'UPDATE' AND pg_trigger_depth() = 1
       AND NEW.parent_ci_id IS NOT DISTINCT FROM OLD.parent_ci_id
       AND OLD.child_mode IS NOT DISTINCT FROM 'configuration'
       AND NEW.location_id IS DISTINCT FROM OLD.location_id
       AND NEW.location_id IS DISTINCT FROM parent_location THEN
        RAISE EXCEPTION 'the location of configuration child % is the location of its parent', NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_location_from_parent';
    END IF;
    NEW.location_id := parent_location;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_ci_configuration_child
    BEFORE INSERT OR UPDATE OF location_id, parent_ci_id, child_mode, client_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_configuration_child();

DROP TRIGGER trg_ci_derive_location ON ci;
CREATE TRIGGER trg_ci_derive_location
    BEFORE INSERT OR UPDATE OF location_id, site_id, room_id, client_id, asset_id, parent_ci_id, child_mode ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_derive_location();

-- A new location of a CI reaches its configuration children, or the change
-- fails. No column list: the location also changes through the BEFORE
-- triggers (a new asset, a new parent).
CREATE FUNCTION ci_propagate_configuration_location() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    previous_team text;
    diverged boolean;
BEGIN
    UPDATE ci SET location_id = NEW.location_id
    WHERE parent_ci_id = NEW.id AND organization_id = NEW.organization_id
      AND child_mode = 'configuration' AND location_id IS DISTINCT FROM NEW.location_id;
    previous := app_scope_widen();
    previous_team := current_setting('app.team_scope', true);
    PERFORM set_config('app.team_scope', '', true);
    SELECT EXISTS (SELECT 1 FROM ci c
                    WHERE c.parent_ci_id = NEW.id AND c.organization_id = NEW.organization_id
                      AND c.child_mode = 'configuration' AND c.location_id IS DISTINCT FROM NEW.location_id)
      INTO diverged;
    PERFORM set_config('app.team_scope', COALESCE(previous_team, ''), true);
    PERFORM app_scope_restore(previous);
    IF diverged THEN
        RAISE EXCEPTION 'a configuration child of CI % is outside the scope of the writer', NEW.id
            USING ERRCODE = '42501', CONSTRAINT = 'ci_configuration_location_scope';
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_ci_propagate_configuration_location AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.location_id IS DISTINCT FROM NEW.location_id)
    EXECUTE FUNCTION ci_propagate_configuration_location();
