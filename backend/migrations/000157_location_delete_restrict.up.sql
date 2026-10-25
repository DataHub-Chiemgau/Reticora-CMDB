-- WP-411 (LOC-11; F-023, F-024, F-227): deleting a location never cascades
-- and never detaches what is bound to it.
--
-- 1. The tree keeps its nodes: location.site_id and location.parent_id
--    (000062), building.site_id, room.building_id and rack.room_id (000001)
--    and room.site_id and rack.site_id (000152) are RESTRICT. Deleting a
--    site, building or room with children is refused with 409 'location' on
--    every delete path (locations/dependencies.go); the foreign keys are the
--    backstop for children the caller cannot see. The site's own location
--    row (site_id = id) is deleted with it and does not block.
-- 2. Network and discovery objects keep their site (E-71, open; transitional
--    rule: option 1): vlan (000091) and agent_enrollment_token (000070) no
--    longer cascade, subnet (000015), collector (000127), discovery_scope
--    (000129) and ci_merge (000131) no longer lose their site. The API
--    refuses the delete with 409 and the kind; expired enrollment tokens and
--    merge tombstones past tombstone_until do not count and are removed in
--    the delete transaction. Another option of E-71 changes the dependency
--    list there and these foreign keys in a follow-up migration.
-- 3. location_change keeps the history of deleted locations: the foreign key
--    to location goes, location_id stays NOT NULL as a reference without key
--    (like ci_merge.survivor_id/victim_id, 000131), and location_name keeps
--    the name of the moved location, backfilled from location.name.
--    location_change stays a tenant table with ENABLE + FORCE RLS.

-- ─── 1. Tree ───────────────────────────────────────────────────────────────────

ALTER TABLE location DROP CONSTRAINT location_site_id_fkey,
    ADD CONSTRAINT location_site_id_fkey FOREIGN KEY (site_id) REFERENCES location(id) ON DELETE RESTRICT,
    DROP CONSTRAINT location_parent_id_fkey,
    ADD CONSTRAINT location_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES location(id) ON DELETE RESTRICT;
ALTER TABLE building DROP CONSTRAINT building_site_id_fkey,
    ADD CONSTRAINT building_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE RESTRICT;
ALTER TABLE room DROP CONSTRAINT room_building_id_fkey,
    ADD CONSTRAINT room_building_id_fkey FOREIGN KEY (building_id) REFERENCES building(id) ON DELETE RESTRICT,
    DROP CONSTRAINT room_site_id_fkey,
    ADD CONSTRAINT room_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE rack DROP CONSTRAINT rack_room_id_fkey,
    ADD CONSTRAINT rack_room_id_fkey FOREIGN KEY (room_id) REFERENCES room(id) ON DELETE RESTRICT,
    DROP CONSTRAINT rack_site_id_fkey,
    ADD CONSTRAINT rack_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE RESTRICT;

-- ─── 2. Objects bound to a site (E-71) ─────────────────────────────────────────

ALTER TABLE vlan DROP CONSTRAINT vlan_site_id_fkey,
    ADD CONSTRAINT vlan_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE RESTRICT;
ALTER TABLE subnet DROP CONSTRAINT subnet_site_id_fkey,
    ADD CONSTRAINT subnet_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE RESTRICT;
ALTER TABLE collector DROP CONSTRAINT collector_site_id_fkey,
    ADD CONSTRAINT collector_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE RESTRICT;
ALTER TABLE discovery_scope DROP CONSTRAINT discovery_scope_site_id_fkey,
    ADD CONSTRAINT discovery_scope_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE RESTRICT;
ALTER TABLE agent_enrollment_token DROP CONSTRAINT agent_enrollment_token_site_id_fkey,
    ADD CONSTRAINT agent_enrollment_token_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE RESTRICT;
ALTER TABLE ci_merge DROP CONSTRAINT ci_merge_site_id_fkey,
    ADD CONSTRAINT ci_merge_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE RESTRICT;

-- ─── 3. location_change ────────────────────────────────────────────────────────

ALTER TABLE location_change DROP CONSTRAINT location_change_location_id_fkey,
    ADD COLUMN location_name TEXT;
UPDATE location_change c SET location_name = l.name FROM location l WHERE l.id = c.location_id;
ALTER TABLE location_change ALTER COLUMN location_name SET NOT NULL;

-- location_record_change writes the change row of a moved location with its
-- name, which stays when the location is deleted later. It runs for the
-- moved node only: the descendants change path but keep their parent.
CREATE OR REPLACE FUNCTION location_record_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO location_change (organization_id, client_id, site_id, location_id, location_name,
                                 old_parent_id, new_parent_id, old_path, new_path, changed_by)
    VALUES (NEW.organization_id, NEW.client_id, NEW.site_id, NEW.id, NEW.name,
            OLD.parent_id, NEW.parent_id, OLD.path::text, NEW.path::text,
            NULLIF(current_setting('app.user_id', true), ''));
    RETURN NULL;
END
$$;
