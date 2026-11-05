-- WP-750 (TEN-02, API-06; E-09, E-40): reservations carry the client and
-- site of their asset or quantity item and are scoped in the database.
--
-- reservation had only the organization policy of 000055; the client and
-- site scope came from the subquery reservationVisible of the repository on
-- asset and quantity_item. Like disposal_record (000145):
--
-- 1. client_id and site_id with foreign keys that include the organization
--    (K19, patterns 000136 and 000151). The BEFORE trigger reservation_scope
--    (derive_reservation_scope) derives both from the asset, else from the
--    quantity item, whatever the writer passes. It reads under the writer's
--    row level security: an object outside the scope derives NULL and WITH
--    CHECK refuses the row for scoped principals (E-09). The site is
--    asset.site_id or quantity_item.site_id (000151, WP-392). Consumables
--    without a quantity_item row derive no scope until WP-284.
-- 2. Propagation: when an asset or quantity item changes client or site,
--    its reservations are re-derived with the client and site scope widened
--    (app_scope_widen, 000139; pattern 000141 and 000145), also those the
--    writer cannot see.
-- 3. Backfill of client_id and site_id; active reservations with
--    reserved_until and without expires_at expire at reserved_until (the
--    rule of WP-277), so the expiry job takes them.
-- 4. Policies like asset in 000151: USING keeps rows without client or site
--    readable, WITH CHECK requires the client and site of the scope unless
--    the dimension is unrestricted (E-09), with the restrictive UPDATE and
--    DELETE policies of 000079. No expression index (K22).

-- ─── 1. Columns and derivation ────────────────────────────────────────────────

ALTER TABLE reservation ADD COLUMN client_id UUID, ADD COLUMN site_id UUID,
    ADD CONSTRAINT reservation_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id),
    ADD CONSTRAINT reservation_site_id_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site (id, organization_id);

CREATE INDEX idx_reservation_client ON reservation (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_reservation_site ON reservation (site_id) WHERE site_id IS NOT NULL;

CREATE FUNCTION derive_reservation_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.asset_id IS NOT NULL THEN
        SELECT a.client_id, a.site_id INTO NEW.client_id, NEW.site_id
          FROM asset a WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
    ELSIF NEW.quantity_item_id IS NOT NULL THEN
        SELECT q.client_id, q.site_id INTO NEW.client_id, NEW.site_id
          FROM quantity_item q WHERE q.id = NEW.quantity_item_id AND q.organization_id = NEW.organization_id;
    END IF;
    RETURN NEW;
END
$$;

-- A change of the state alone does not re-derive: the scope follows the
-- object through the propagation below.
CREATE TRIGGER reservation_scope
    BEFORE INSERT OR UPDATE OF organization_id, asset_id, quantity_item_id, client_id, site_id ON reservation
    FOR EACH ROW EXECUTE FUNCTION derive_reservation_scope();

-- ─── 2. Propagation ────────────────────────────────────────────────────────────

CREATE FUNCTION propagate_reservation_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'asset' THEN
        UPDATE reservation SET asset_id = asset_id
         WHERE asset_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE reservation SET quantity_item_id = quantity_item_id
         WHERE quantity_item_id = NEW.id AND organization_id = NEW.organization_id AND asset_id IS NULL;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER asset_reservation_scope_propagation AFTER UPDATE ON asset
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_reservation_scope();
CREATE TRIGGER quantity_item_reservation_scope_propagation AFTER UPDATE ON quantity_item
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_reservation_scope();

-- ─── 3. Backfill ───────────────────────────────────────────────────────────────

-- The migration runs without row level security; updated_at stays.
ALTER TABLE reservation DISABLE TRIGGER trg_reservation_updated_at;
UPDATE reservation SET asset_id = asset_id;
UPDATE reservation SET expires_at = reserved_until
 WHERE state = 'active' AND expires_at IS NULL AND reserved_until IS NOT NULL;
ALTER TABLE reservation ENABLE TRIGGER trg_reservation_updated_at;

-- ─── 4. Policies ───────────────────────────────────────────────────────────────

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    writable text := format('%1$s AND (%2$s OR client_id = ANY (%3$s)) AND (%4$s OR site_id = ANY (%5$s))',
                            org_match, client_all, client_ids, site_all, site_ids);
BEGIN
    DROP POLICY reservation_isolation ON reservation;
    EXECUTE format(
        'CREATE POLICY reservation_isolation ON reservation '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY reservation_scoped_update ON reservation AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY reservation_scoped_delete ON reservation AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;
