-- WP-055 (LOC-11): deleting a location never silently drops what references
-- it, and every move is recorded.
--
-- 1. References to a location or rack are RESTRICT: CIs, assets, stock
--    items, movement ledger rows and rack mounts. The API refuses such a
--    delete with 409 and the kinds of the dependent objects; the foreign keys
--    are the backstop for rows the caller cannot see.
-- 2. location_change records every re-parenting of a location, whichever
--    path moved it (location API, site/building/room/rack tables): old and
--    new parent and path and the acting user. Tenant table with RLS (TEN-05).

ALTER TABLE ci DROP CONSTRAINT ci_location_id_fkey,
    ADD CONSTRAINT ci_location_id_fkey FOREIGN KEY (location_id) REFERENCES location(id) ON DELETE RESTRICT;
ALTER TABLE asset DROP CONSTRAINT asset_location_id_fkey,
    ADD CONSTRAINT asset_location_id_fkey FOREIGN KEY (location_id) REFERENCES location(id) ON DELETE RESTRICT;
ALTER TABLE quantity_item DROP CONSTRAINT quantity_item_location_id_fkey,
    ADD CONSTRAINT quantity_item_location_id_fkey FOREIGN KEY (location_id) REFERENCES location(id) ON DELETE RESTRICT;
-- The ledger is append-only: SET NULL could never run.
ALTER TABLE asset_movement DROP CONSTRAINT asset_movement_from_location_id_fkey,
    ADD CONSTRAINT asset_movement_from_location_id_fkey FOREIGN KEY (from_location_id) REFERENCES location(id) ON DELETE RESTRICT;
ALTER TABLE asset_movement DROP CONSTRAINT asset_movement_to_location_id_fkey,
    ADD CONSTRAINT asset_movement_to_location_id_fkey FOREIGN KEY (to_location_id) REFERENCES location(id) ON DELETE RESTRICT;
ALTER TABLE rack_mount DROP CONSTRAINT rack_mount_rack_id_fkey,
    ADD CONSTRAINT rack_mount_rack_id_fkey FOREIGN KEY (rack_id) REFERENCES rack(id) ON DELETE RESTRICT;

CREATE TABLE location_change (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id       UUID NOT NULL,
    site_id         UUID NOT NULL,
    location_id     UUID NOT NULL REFERENCES location(id) ON DELETE CASCADE,
    old_parent_id   UUID,
    new_parent_id   UUID,
    old_path        TEXT NOT NULL,
    new_path        TEXT NOT NULL,
    changed_by      TEXT,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_location_change_location ON location_change (location_id, changed_at DESC);
CREATE INDEX idx_location_change_org ON location_change (organization_id, changed_at DESC);

ALTER TABLE location_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE location_change FORCE ROW LEVEL SECURITY;
CREATE POLICY location_change_isolation ON location_change
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

-- location_record_change writes the change row of a moved location. It runs
-- for the moved node only: the descendants change path but keep their parent.
CREATE FUNCTION location_record_change() RETURNS trigger
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

CREATE TRIGGER trg_location_record_change AFTER UPDATE OF parent_id ON location
    FOR EACH ROW WHEN (OLD.parent_id IS DISTINCT FROM NEW.parent_id)
    EXECUTE FUNCTION location_record_change();
