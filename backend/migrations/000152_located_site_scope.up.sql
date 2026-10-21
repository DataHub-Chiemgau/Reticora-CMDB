-- WP-393 (TEN-02, TEN-05, TEN-09; F-053): rooms and racks carry the client
-- and site of their location, and search hits of assets and reservations
-- the site of their item.
--
-- room and rack share their id with their location row (000062) but had no
-- scope column; their policies (000018) checked the organization only. A
-- principal restricted to site 1 read, changed and deleted the rooms and
-- racks of every site directly (the API filtered them in Go only). The
-- search rows of assets and reservations took the client of the asset only
-- (000067), so a site-scoped principal found the assets of every site of
-- its clients in search_document and the remote index, and reservations of
-- quantity items were org-wide.
--
-- 1. client_id and site_id on room and rack (foreign keys to client and site
--    of the organization), backfilled from the location row. Every room and
--    rack has a location row with client and site, so the columns are NOT
--    NULL and there are no org-wide rows (E-09 holds by construction).
-- 2. A BEFORE trigger derives both from the location row, a written value is
--    overwritten. The location is read with the client and site scope
--    widened (app_scope_widen, 000139, like derive_location_site in 000151);
--    the policies are checked with the scope of the writer, whose location
--    row already passed the location policy.
-- 3. When the location row of a room or rack changes client or site (moved,
--    re-parented, the descendants through location_propagate, or its site
--    changed client), the room or rack is re-derived with the scope widened.
-- 4. The policies of room and rack get the client and site predicate of
--    location (000062) in USING and WITH CHECK (TEN-05). ENABLE and FORCE
--    ROW LEVEL SECURITY stay.
-- 5. derive_search_document_scope takes the site of the asset (000151) for
--    assets and reservations, and client and site of the quantity item for
--    reservations of quantity items; the search rows follow a changed site
--    of an asset, a changed client or site of a quantity item and a changed
--    item of a reservation.

-- ─── 1. Columns and backfill ──────────────────────────────────────────────────

ALTER TABLE room ADD COLUMN client_id UUID, ADD COLUMN site_id UUID,
    ADD CONSTRAINT room_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    ADD CONSTRAINT room_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE CASCADE;
ALTER TABLE rack ADD COLUMN client_id UUID, ADD COLUMN site_id UUID,
    ADD CONSTRAINT rack_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    ADD CONSTRAINT rack_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id) ON DELETE CASCADE;

-- Before the derivation trigger exists; the updated_at triggers stay off, the
-- rows do not change for their users.
ALTER TABLE room DISABLE TRIGGER trg_room_updated_at;
ALTER TABLE rack DISABLE TRIGGER trg_rack_updated_at;
UPDATE room r SET client_id = l.client_id, site_id = l.site_id
  FROM location l WHERE l.id = r.id AND l.organization_id = r.organization_id;
UPDATE rack r SET client_id = l.client_id, site_id = l.site_id
  FROM location l WHERE l.id = r.id AND l.organization_id = r.organization_id;
ALTER TABLE room ENABLE TRIGGER trg_room_updated_at;
ALTER TABLE rack ENABLE TRIGGER trg_rack_updated_at;

-- The location row is created by the trigger of the specialist table with
-- its organization (000062); a row without one is inconsistent data.
DO $$
DECLARE
    orphaned bigint;
BEGIN
    SELECT (SELECT count(*) FROM room WHERE site_id IS NULL) + (SELECT count(*) FROM rack WHERE site_id IS NULL)
      INTO orphaned;
    IF orphaned > 0 THEN
        RAISE EXCEPTION '% rooms and racks have no location row of their organization', orphaned;
    END IF;
END
$$;

ALTER TABLE room ALTER COLUMN client_id SET NOT NULL, ALTER COLUMN site_id SET NOT NULL;
ALTER TABLE rack ALTER COLUMN client_id SET NOT NULL, ALTER COLUMN site_id SET NOT NULL;

CREATE INDEX idx_room_client ON room (client_id);
CREATE INDEX idx_room_site ON room (site_id);
CREATE INDEX idx_rack_client ON rack (client_id);
CREATE INDEX idx_rack_site ON rack (site_id);

-- ─── 2. Derivation ─────────────────────────────────────────────────────────────

-- derive_location_row_scope sets client_id and site_id of a room or rack to
-- those of its location row (same id). On INSERT it runs after the trigger
-- that creates the location row (trg_<table>_location_insert, triggers fire
-- in the order of their names).
CREATE FUNCTION derive_location_row_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    located boolean;
BEGIN
    SELECT l.client_id, l.site_id INTO NEW.client_id, NEW.site_id
      FROM location l WHERE l.id = NEW.id AND l.organization_id = NEW.organization_id;
    located := FOUND;
    PERFORM app_scope_restore(previous);
    IF NOT located THEN
        RAISE EXCEPTION 'location of % % not found', TG_TABLE_NAME, NEW.id
            USING ERRCODE = '23503', CONSTRAINT = TG_TABLE_NAME || '_location_fkey';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_room_location_scope BEFORE INSERT OR UPDATE ON room
    FOR EACH ROW EXECUTE FUNCTION derive_location_row_scope();
CREATE TRIGGER trg_rack_location_scope BEFORE INSERT OR UPDATE ON rack
    FOR EACH ROW EXECUTE FUNCTION derive_location_row_scope();

-- ─── 3. Propagation ────────────────────────────────────────────────────────────

-- location_refresh_room_rack_scope re-derives the room or rack of a location
-- row whose client or site changed.
CREATE FUNCTION location_refresh_room_rack_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF NEW.kind = 'room' THEN
        UPDATE room SET site_id = site_id WHERE id = NEW.id;
    ELSE
        UPDATE rack SET site_id = site_id WHERE id = NEW.id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_location_refresh_room_rack_scope AFTER UPDATE ON location
    FOR EACH ROW
    WHEN (NEW.kind IN ('room', 'rack')
          AND (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id))
    EXECUTE FUNCTION location_refresh_room_rack_scope();

-- ─── 4. Policies ───────────────────────────────────────────────────────────────

DROP POLICY room_isolation ON room;
CREATE POLICY room_isolation ON room
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

DROP POLICY rack_isolation ON rack;
CREATE POLICY rack_isolation ON rack
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

-- ─── 5. Search rows of assets and reservations ────────────────────────────────

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
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id FROM asset a WHERE a.id = NEW.entity_id;
        WHEN 'contact' THEN
            SELECT c.client_id INTO NEW.client_id FROM contact c WHERE c.id = NEW.entity_id;
        WHEN 'reservation' THEN
            -- An asset reservation has the scope of its asset, any other the
            -- scope of its quantity item.
            SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
              FROM reservation r JOIN asset a ON a.id = r.asset_id WHERE r.id = NEW.entity_id;
            IF NOT FOUND THEN
                SELECT q.client_id, q.site_id INTO NEW.client_id, NEW.site_id
                  FROM reservation r JOIN quantity_item q ON q.id = r.quantity_item_id WHERE r.id = NEW.entity_id;
            END IF;
        ELSE
            NULL; -- document, compliance: org-wide
    END CASE;
    RETURN NEW;
END
$$;

-- propagate_search_scope re-derives the search rows of the changed entity;
-- for an asset or quantity item also those of its reservations.
CREATE OR REPLACE FUNCTION propagate_search_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE search_document SET entity_id = entity_id
     WHERE entity_type = TG_ARGV[0] AND entity_id = NEW.id;
    IF TG_ARGV[0] = 'asset' THEN
        UPDATE search_document SET entity_id = entity_id
         WHERE entity_type = 'reservation'
           AND entity_id IN (SELECT r.id FROM reservation r WHERE r.asset_id = NEW.id);
    ELSIF TG_ARGV[0] = 'quantity_item' THEN
        UPDATE search_document SET entity_id = entity_id
         WHERE entity_type = 'reservation'
           AND entity_id IN (SELECT r.id FROM reservation r WHERE r.quantity_item_id = NEW.id);
    END IF;
    RETURN NULL;
END
$$;

DROP TRIGGER asset_search_scope_propagation ON asset;
CREATE TRIGGER asset_search_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('asset');
CREATE TRIGGER quantity_item_search_scope_propagation AFTER UPDATE ON quantity_item
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_search_scope('quantity_item');
DROP TRIGGER reservation_search_scope_propagation ON reservation;
CREATE TRIGGER reservation_search_scope_propagation AFTER UPDATE ON reservation
    FOR EACH ROW WHEN (OLD.asset_id IS DISTINCT FROM NEW.asset_id OR OLD.quantity_item_id IS DISTINCT FROM NEW.quantity_item_id)
    EXECUTE FUNCTION propagate_search_scope('reservation');

UPDATE search_document SET entity_id = entity_id WHERE entity_type IN ('asset', 'reservation');
