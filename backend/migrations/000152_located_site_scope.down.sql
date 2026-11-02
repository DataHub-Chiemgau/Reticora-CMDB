-- Reverts WP-393: rooms and racks without client and site, organization-only
-- policies of migration 000018; search rows of assets and reservations with
-- the client of the asset only (000067, 000074).

DROP TRIGGER reservation_search_scope_propagation ON reservation;
CREATE TRIGGER reservation_search_scope_propagation AFTER UPDATE ON reservation
    FOR EACH ROW WHEN (OLD.asset_id IS DISTINCT FROM NEW.asset_id)
    EXECUTE FUNCTION propagate_search_scope('reservation');
DROP TRIGGER quantity_item_search_scope_propagation ON quantity_item;
DROP TRIGGER asset_search_scope_propagation ON asset;
CREATE TRIGGER asset_search_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id)
    EXECUTE FUNCTION propagate_search_scope('asset');

CREATE OR REPLACE FUNCTION propagate_search_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE search_document SET entity_id = entity_id
     WHERE entity_type = TG_ARGV[0] AND entity_id = NEW.id;
    IF TG_ARGV[0] = 'asset' THEN
        UPDATE search_document SET entity_id = entity_id
         WHERE entity_type = 'reservation'
           AND entity_id IN (SELECT r.id FROM reservation r WHERE r.asset_id = NEW.id);
    END IF;
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION derive_search_document_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    CASE NEW.entity_type
        WHEN 'ci' THEN
            SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.entity_id;
        WHEN 'ticket' THEN
            SELECT t.client_id, t.site_id INTO NEW.client_id, NEW.site_id FROM ticket t WHERE t.id = NEW.entity_id;
        WHEN 'location' THEN
            SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id FROM location l WHERE l.id = NEW.entity_id;
        WHEN 'asset' THEN
            SELECT a.client_id INTO NEW.client_id FROM asset a WHERE a.id = NEW.entity_id;
        WHEN 'contact' THEN
            SELECT c.client_id INTO NEW.client_id FROM contact c WHERE c.id = NEW.entity_id;
        WHEN 'reservation' THEN
            SELECT a.client_id INTO NEW.client_id
              FROM reservation r JOIN asset a ON a.id = r.asset_id WHERE r.id = NEW.entity_id;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

UPDATE search_document SET entity_id = entity_id WHERE entity_type IN ('asset', 'reservation');

DROP POLICY rack_isolation ON rack;
CREATE POLICY rack_isolation ON rack
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
DROP POLICY room_isolation ON room;
CREATE POLICY room_isolation ON room
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP TRIGGER trg_location_refresh_room_rack_scope ON location;
DROP FUNCTION location_refresh_room_rack_scope();
DROP TRIGGER trg_rack_location_scope ON rack;
DROP TRIGGER trg_room_location_scope ON room;
DROP FUNCTION derive_location_row_scope();

DROP INDEX idx_rack_site;
DROP INDEX idx_rack_client;
DROP INDEX idx_room_site;
DROP INDEX idx_room_client;
ALTER TABLE rack DROP COLUMN site_id, DROP COLUMN client_id;
ALTER TABLE room DROP COLUMN site_id, DROP COLUMN client_id;
