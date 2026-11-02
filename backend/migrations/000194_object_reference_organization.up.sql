-- Migration 000194: the references of the phase-2 modules on ci, asset,
-- location and consumable stay in their organization (WP-838, SR-03; ABN-03,
-- TEN-09).
--
-- PostgreSQL checks foreign keys without row level security. A reference
-- that names the referenced row only (REFERENCES … (id)) accepts the id of an
-- object of another organization: the database alone does not keep the
-- organizations apart (SR-03). Since 000136 every reference to client carries
-- the organization; ci and asset have UNIQUE (id, organization_id) since
-- 000056. The twelve references below become composite foreign keys
-- (column, organization_id) REFERENCES … (id, organization_id): a reference
-- into another organization fails like an unknown id (23503). The
-- constraint names and the ON DELETE actions stay (SET NULL as
-- SET NULL (column), pattern 000136), so the error mapping of the
-- repositories is unchanged (ci_location_id_fkey → 422 in
-- ci.locationError, pattern ci_asset_id_fkey of 000088). location and
-- consumable get UNIQUE (id, organization_id) for the composite keys.
-- asset.parent_asset_id is not changed: the column goes with WP-743. The
-- remaining references without organization are listed with their reason
-- in rls.ReferencesWithoutOrganization (TestTenantReferencesCarryOrganization).
--
-- Existing rows whose reference crosses the organization are not deleted
-- (E-26): a nullable reference is recorded in migration_quarantine with the
-- whole row and set to NULL, a reservation (asset_id is its item) moves to
-- migration_quarantine. The down migration gives them back.

-- The location tree has checked the organization of the parent since 000062
-- (location_maintain, which also derives site_id from the parent). A node
-- can neither lose its parent (location_root_is_site) nor leave the tree
-- without its subtree, the assets and CIs placed in it and its specialist
-- row. A crossing node can only come from a write that bypassed the
-- triggers; the migration stops with the list instead of changing the tree
-- (pattern 000177), the node has to be corrected by hand.
DO $$
DECLARE
    crossing text;
BEGIN
    SELECT string_agg(c.r, ', ' ORDER BY c.r) INTO crossing FROM (
        SELECT 'location ' || l.id || ' parent_id' FROM location l JOIN location x ON x.id = l.parent_id
         WHERE x.organization_id <> l.organization_id
        UNION ALL
        SELECT 'location ' || l.id || ' site_id' FROM location l JOIN location x ON x.id = l.site_id
         WHERE x.organization_id <> l.organization_id
    ) AS c(r);
    IF crossing IS NOT NULL THEN
        RAISE EXCEPTION 'locations referencing another organization, correct them before migration 000194: %', crossing;
    END IF;
END
$$;

-- Every crossing reference is recorded before the first change: the change
-- of an asset's location also moves the location of its CIs
-- (asset_propagate_location), whose rows must be recorded as they were.
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'asset', to_jsonb(t), 'SR-03 (WP-838): asset.location_id references another organization, set to NULL', '000194'
  FROM asset t JOIN location x ON x.id = t.location_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'asset', to_jsonb(t), 'SR-03 (WP-838): asset.ci_id references another organization, set to NULL', '000194'
  FROM asset t JOIN ci x ON x.id = t.ci_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ci', to_jsonb(t), 'SR-03 (WP-838): ci.location_id references another organization, set to NULL', '000194'
  FROM ci t JOIN location x ON x.id = t.location_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ticket', to_jsonb(t), 'SR-03 (WP-838): ticket.related_ci_id references another organization, set to NULL', '000194'
  FROM ticket t JOIN ci x ON x.id = t.related_ci_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ticket', to_jsonb(t), 'SR-03 (WP-838): ticket.related_asset_id references another organization, set to NULL', '000194'
  FROM ticket t JOIN asset x ON x.id = t.related_asset_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'disposal_record', to_jsonb(t), 'SR-03 (WP-838): disposal_record.asset_id references another organization, set to NULL', '000194'
  FROM disposal_record t JOIN asset x ON x.id = t.asset_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'disposal_record', to_jsonb(t), 'SR-03 (WP-838): disposal_record.ci_id references another organization, set to NULL', '000194'
  FROM disposal_record t JOIN ci x ON x.id = t.ci_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'internal_order_item', to_jsonb(t), 'SR-03 (WP-838): internal_order_item.asset_id references another organization, set to NULL', '000194'
  FROM internal_order_item t JOIN asset x ON x.id = t.asset_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'internal_order_item', to_jsonb(t), 'SR-03 (WP-838): internal_order_item.consumable_id references another organization, set to NULL', '000194'
  FROM internal_order_item t JOIN consumable x ON x.id = t.consumable_id WHERE x.organization_id <> t.organization_id;

-- The references are cleared through the regular triggers, which derive
-- the scope columns (site_id, room_id, client_id) again from what is left.
UPDATE asset t SET location_id = NULL
  FROM location x WHERE x.id = t.location_id AND x.organization_id <> t.organization_id;
UPDATE asset t SET ci_id = NULL
  FROM ci x WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id;
UPDATE ci t SET location_id = NULL
  FROM location x WHERE x.id = t.location_id AND x.organization_id <> t.organization_id;
UPDATE ticket t SET related_ci_id = NULL
  FROM ci x WHERE x.id = t.related_ci_id AND x.organization_id <> t.organization_id;
UPDATE ticket t SET related_asset_id = NULL
  FROM asset x WHERE x.id = t.related_asset_id AND x.organization_id <> t.organization_id;
-- disposal_record is append-only; its guard is lifted for the correction
-- only (pattern 000145), the scope trigger stays active.
ALTER TABLE disposal_record DISABLE TRIGGER disposal_record_no_update;
UPDATE disposal_record t SET asset_id = NULL
  FROM asset x WHERE x.id = t.asset_id AND x.organization_id <> t.organization_id;
UPDATE disposal_record t SET ci_id = NULL
  FROM ci x WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id;
ALTER TABLE disposal_record ENABLE TRIGGER disposal_record_no_update;
UPDATE internal_order_item t SET asset_id = NULL
  FROM asset x WHERE x.id = t.asset_id AND x.organization_id <> t.organization_id;
UPDATE internal_order_item t SET consumable_id = NULL
  FROM consumable x WHERE x.id = t.consumable_id AND x.organization_id <> t.organization_id;

-- A reservation without its asset is no reservation (reservation_item_check):
-- it moves to migration_quarantine.
WITH moved AS (
    DELETE FROM reservation t
     USING asset x
     WHERE x.id = t.asset_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'reservation', to_jsonb(moved),
       'SR-03 (WP-838): reservation.asset_id references another organization', '000194'
  FROM moved;

ALTER TABLE location ADD CONSTRAINT location_id_organization_key UNIQUE (id, organization_id);
ALTER TABLE consumable ADD CONSTRAINT consumable_id_organization_key UNIQUE (id, organization_id);

ALTER TABLE asset DROP CONSTRAINT asset_ci_id_fkey,
    ADD CONSTRAINT asset_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id),
    DROP CONSTRAINT asset_location_id_fkey,
    ADD CONSTRAINT asset_location_id_fkey FOREIGN KEY (location_id, organization_id) REFERENCES location (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE ci DROP CONSTRAINT ci_location_id_fkey,
    ADD CONSTRAINT ci_location_id_fkey FOREIGN KEY (location_id, organization_id) REFERENCES location (id, organization_id) ON DELETE RESTRICT;
ALTER TABLE ticket DROP CONSTRAINT ticket_related_ci_id_fkey,
    ADD CONSTRAINT ticket_related_ci_id_fkey FOREIGN KEY (related_ci_id, organization_id) REFERENCES ci (id, organization_id),
    DROP CONSTRAINT ticket_related_asset_id_fkey,
    ADD CONSTRAINT ticket_related_asset_id_fkey FOREIGN KEY (related_asset_id, organization_id) REFERENCES asset (id, organization_id);
ALTER TABLE reservation DROP CONSTRAINT reservation_asset_id_fkey,
    ADD CONSTRAINT reservation_asset_id_fkey FOREIGN KEY (asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE CASCADE;
ALTER TABLE disposal_record DROP CONSTRAINT disposal_record_asset_id_fkey,
    ADD CONSTRAINT disposal_record_asset_id_fkey FOREIGN KEY (asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE SET NULL (asset_id),
    DROP CONSTRAINT disposal_record_ci_id_fkey,
    ADD CONSTRAINT disposal_record_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id);
ALTER TABLE internal_order_item DROP CONSTRAINT internal_order_item_asset_id_fkey,
    ADD CONSTRAINT internal_order_item_asset_id_fkey FOREIGN KEY (asset_id, organization_id) REFERENCES asset (id, organization_id) ON DELETE SET NULL (asset_id),
    DROP CONSTRAINT internal_order_item_consumable_id_fkey,
    ADD CONSTRAINT internal_order_item_consumable_id_fkey FOREIGN KEY (consumable_id, organization_id) REFERENCES consumable (id, organization_id) ON DELETE SET NULL (consumable_id);
ALTER TABLE location DROP CONSTRAINT location_site_id_fkey,
    ADD CONSTRAINT location_site_id_fkey FOREIGN KEY (site_id, organization_id) REFERENCES location (id, organization_id) ON DELETE RESTRICT,
    DROP CONSTRAINT location_parent_id_fkey,
    ADD CONSTRAINT location_parent_id_fkey FOREIGN KEY (parent_id, organization_id) REFERENCES location (id, organization_id) ON DELETE RESTRICT;
