-- Reverts 000157: the delete rules of 000001, 000015, 000062, 000070,
-- 000075, 000091, 000127, 000129, 000131 and 000152.

CREATE OR REPLACE FUNCTION location_record_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO location_change (organization_id, client_id, site_id, location_id,
                                 old_parent_id, new_parent_id, old_path, new_path, changed_by)
    VALUES (NEW.organization_id, NEW.client_id, NEW.site_id, NEW.id,
            OLD.parent_id, NEW.parent_id, OLD.path::text, NEW.path::text,
            NULLIF(current_setting('app.user_id', true), ''));
    RETURN NULL;
END
$$;

-- The foreign key with CASCADE cannot hold the history of deleted
-- locations: those change rows are lost on the way down.
DELETE FROM location_change c WHERE NOT EXISTS (SELECT 1 FROM location l WHERE l.id = c.location_id);
ALTER TABLE location_change DROP COLUMN location_name,
    ADD CONSTRAINT location_change_location_id_fkey FOREIGN KEY (location_id) REFERENCES location(id) ON DELETE CASCADE;

ALTER TABLE ci_merge DROP CONSTRAINT ci_merge_site_id_fkey,
    ADD CONSTRAINT ci_merge_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE SET NULL;
ALTER TABLE agent_enrollment_token DROP CONSTRAINT agent_enrollment_token_site_id_fkey,
    ADD CONSTRAINT agent_enrollment_token_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE CASCADE;
ALTER TABLE discovery_scope DROP CONSTRAINT discovery_scope_site_id_fkey,
    ADD CONSTRAINT discovery_scope_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE SET NULL;
ALTER TABLE collector DROP CONSTRAINT collector_site_id_fkey,
    ADD CONSTRAINT collector_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE SET NULL;
ALTER TABLE subnet DROP CONSTRAINT subnet_site_id_fkey,
    ADD CONSTRAINT subnet_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE SET NULL;
ALTER TABLE vlan DROP CONSTRAINT vlan_site_id_fkey,
    ADD CONSTRAINT vlan_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE CASCADE;

ALTER TABLE rack DROP CONSTRAINT rack_site_id_fkey,
    ADD CONSTRAINT rack_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT rack_room_id_fkey,
    ADD CONSTRAINT rack_room_id_fkey FOREIGN KEY (room_id) REFERENCES room(id) ON DELETE CASCADE;
ALTER TABLE room DROP CONSTRAINT room_site_id_fkey,
    ADD CONSTRAINT room_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT room_building_id_fkey,
    ADD CONSTRAINT room_building_id_fkey FOREIGN KEY (building_id) REFERENCES building(id) ON DELETE CASCADE;
ALTER TABLE building DROP CONSTRAINT building_site_id_fkey,
    ADD CONSTRAINT building_site_id_fkey FOREIGN KEY (site_id) REFERENCES site(id) ON DELETE CASCADE;
ALTER TABLE location DROP CONSTRAINT location_parent_id_fkey,
    ADD CONSTRAINT location_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES location(id) ON DELETE CASCADE,
    DROP CONSTRAINT location_site_id_fkey,
    ADD CONSTRAINT location_site_id_fkey FOREIGN KEY (site_id) REFERENCES location(id) ON DELETE CASCADE;
