-- Migration 000172: highest occupied unit of a rack over all of its mounts
-- (WP-634, RCK-01, TEN-06; F-360).
--
-- PATCH /racks/{id} refuses a height below the highest unit occupied by a
-- mount (WP-500). The repository read rack_mount under the RLS of the
-- caller, and rack_mount_isolation filters by the client and site of the
-- mounted CI: a principal restricted to client 1 did not see the mount of a
-- client-2 CI in a rack of client 1 and shrank the rack below it.
--
-- rack_highest_mounted_unit(rack) returns that unit (0 for a rack without
-- mounts) over every mount of the rack, also those the caller cannot see. It
-- returns NULL for a rack the caller cannot see: the rack is looked up under
-- the caller's scope (policies of rack and location) before anything else.
-- Only then are the client and site scope widened (app_scope_widen, 000139)
-- for the one aggregate; app.org_id stays, so the function reaches no row
-- outside the caller's organization, and it returns a number, no data of a
-- mount (no CI, no position of a single mount).
--
-- SECURITY DEFINER with the table owner reticora_owner (NOSUPERUSER,
-- NOBYPASSRLS) and a fixed search_path: the tables keep FORCE ROW LEVEL
-- SECURITY, so the policies of rack, location and rack_mount apply inside the
-- function as well; the widened scope, not the owner, makes the foreign
-- mounts visible. The policies of rack and rack_mount are unchanged.

CREATE FUNCTION rack_highest_mounted_unit(p_rack uuid) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    top integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM rack r
                    WHERE r.id = p_rack AND EXISTS (SELECT 1 FROM location l WHERE l.id = r.id)) THEN
        RETURN NULL;
    END IF;
    previous := app_scope_widen();
    SELECT COALESCE(MAX(m.position_u + m.height_u - 1), 0) INTO top
      FROM rack_mount m WHERE m.rack_id = p_rack;
    PERFORM app_scope_restore(previous);
    RETURN top;
END
$$;

ALTER FUNCTION rack_highest_mounted_unit(uuid) OWNER TO reticora_owner;
REVOKE ALL ON FUNCTION rack_highest_mounted_unit(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION rack_highest_mounted_unit(uuid) TO reticora_app;
