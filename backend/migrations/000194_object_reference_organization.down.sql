-- Reverts WP-838: the twelve references name the referenced row only again
-- (000009, 000013, 000046, 000049, 000055, 000062, 000075, 000157), location
-- and consumable lose UNIQUE (id, organization_id), and the rows the up
-- migration changed come back from migration_quarantine.

ALTER TABLE location DROP CONSTRAINT location_parent_id_fkey,
    ADD CONSTRAINT location_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES location (id) ON DELETE RESTRICT,
    DROP CONSTRAINT location_site_id_fkey,
    ADD CONSTRAINT location_site_id_fkey FOREIGN KEY (site_id) REFERENCES location (id) ON DELETE RESTRICT;
ALTER TABLE internal_order_item DROP CONSTRAINT internal_order_item_consumable_id_fkey,
    ADD CONSTRAINT internal_order_item_consumable_id_fkey FOREIGN KEY (consumable_id) REFERENCES consumable (id) ON DELETE SET NULL,
    DROP CONSTRAINT internal_order_item_asset_id_fkey,
    ADD CONSTRAINT internal_order_item_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES asset (id) ON DELETE SET NULL;
ALTER TABLE disposal_record DROP CONSTRAINT disposal_record_ci_id_fkey,
    ADD CONSTRAINT disposal_record_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE SET NULL,
    DROP CONSTRAINT disposal_record_asset_id_fkey,
    ADD CONSTRAINT disposal_record_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES asset (id) ON DELETE SET NULL;
ALTER TABLE reservation DROP CONSTRAINT reservation_asset_id_fkey,
    ADD CONSTRAINT reservation_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES asset (id) ON DELETE CASCADE;
ALTER TABLE ticket DROP CONSTRAINT ticket_related_asset_id_fkey,
    ADD CONSTRAINT ticket_related_asset_id_fkey FOREIGN KEY (related_asset_id) REFERENCES asset (id),
    DROP CONSTRAINT ticket_related_ci_id_fkey,
    ADD CONSTRAINT ticket_related_ci_id_fkey FOREIGN KEY (related_ci_id) REFERENCES ci (id);
ALTER TABLE ci DROP CONSTRAINT ci_location_id_fkey,
    ADD CONSTRAINT ci_location_id_fkey FOREIGN KEY (location_id) REFERENCES location (id) ON DELETE RESTRICT;
ALTER TABLE asset DROP CONSTRAINT asset_location_id_fkey,
    ADD CONSTRAINT asset_location_id_fkey FOREIGN KEY (location_id) REFERENCES location (id) ON DELETE RESTRICT,
    DROP CONSTRAINT asset_ci_id_fkey,
    ADD CONSTRAINT asset_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id);

ALTER TABLE consumable DROP CONSTRAINT consumable_id_organization_key;
ALTER TABLE location DROP CONSTRAINT location_id_organization_key;

-- The recorded columns come back exactly as they were, together with the
-- scope columns the triggers derived from them. The triggers of the tables
-- would refuse a reference into another organization (derive_location_site,
-- ci_mirror_asset_location, the append-only guard of disposal_record) or
-- write it on (asset_sync_primary_ci), so they are off while the rows are
-- restored; the foreign keys stay checked. A reference whose target has
-- been deleted since stays NULL.
ALTER TABLE asset DISABLE TRIGGER USER;
ALTER TABLE ci DISABLE TRIGGER USER;
ALTER TABLE ticket DISABLE TRIGGER USER;
ALTER TABLE disposal_record DISABLE TRIGGER USER;

UPDATE asset t SET location_id = (q.row_data ->> 'location_id')::uuid, site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'asset'
   AND q.reason = 'SR-03 (WP-838): asset.location_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM location x WHERE x.id = (q.row_data ->> 'location_id')::uuid);
UPDATE asset t SET ci_id = (q.row_data ->> 'ci_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'asset'
   AND q.reason = 'SR-03 (WP-838): asset.ci_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = (q.row_data ->> 'ci_id')::uuid);
UPDATE ci t SET location_id = (q.row_data ->> 'location_id')::uuid, site_id = (q.row_data ->> 'site_id')::uuid,
                room_id = (q.row_data ->> 'room_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'ci'
   AND q.reason = 'SR-03 (WP-838): ci.location_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM location x WHERE x.id = (q.row_data ->> 'location_id')::uuid);
UPDATE ticket t SET related_ci_id = (q.row_data ->> 'related_ci_id')::uuid,
                    client_id = (q.row_data ->> 'client_id')::uuid, site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'ticket'
   AND q.reason = 'SR-03 (WP-838): ticket.related_ci_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = (q.row_data ->> 'related_ci_id')::uuid);
UPDATE ticket t SET related_asset_id = (q.row_data ->> 'related_asset_id')::uuid,
                    client_id = (q.row_data ->> 'client_id')::uuid, site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'ticket'
   AND q.reason = 'SR-03 (WP-838): ticket.related_asset_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM asset x WHERE x.id = (q.row_data ->> 'related_asset_id')::uuid);
UPDATE disposal_record t SET asset_id = (q.row_data ->> 'asset_id')::uuid,
                             client_id = (q.row_data ->> 'client_id')::uuid, site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'disposal_record'
   AND q.reason = 'SR-03 (WP-838): disposal_record.asset_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM asset x WHERE x.id = (q.row_data ->> 'asset_id')::uuid);
UPDATE disposal_record t SET ci_id = (q.row_data ->> 'ci_id')::uuid,
                             client_id = (q.row_data ->> 'client_id')::uuid, site_id = (q.row_data ->> 'site_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'disposal_record'
   AND q.reason = 'SR-03 (WP-838): disposal_record.ci_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = (q.row_data ->> 'ci_id')::uuid);

ALTER TABLE disposal_record ENABLE TRIGGER USER;
ALTER TABLE ticket ENABLE TRIGGER USER;
ALTER TABLE ci ENABLE TRIGGER USER;
ALTER TABLE asset ENABLE TRIGGER USER;

UPDATE internal_order_item t SET asset_id = (q.row_data ->> 'asset_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'internal_order_item'
   AND q.reason = 'SR-03 (WP-838): internal_order_item.asset_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM asset x WHERE x.id = (q.row_data ->> 'asset_id')::uuid);
UPDATE internal_order_item t SET consumable_id = (q.row_data ->> 'consumable_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000194' AND q.source_table = 'internal_order_item'
   AND q.reason = 'SR-03 (WP-838): internal_order_item.consumable_id references another organization, set to NULL'
   AND t.id = (q.row_data ->> 'id')::uuid
   AND EXISTS (SELECT 1 FROM consumable x WHERE x.id = (q.row_data ->> 'consumable_id')::uuid);

-- A reservation whose organization, asset or quantity item has been
-- deleted since cannot come back.
INSERT INTO reservation
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::reservation, q.row_data) AS r
 WHERE q.migration = '000194' AND q.source_table = 'reservation'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM asset x WHERE x.id = r.asset_id)
   AND (r.quantity_item_id IS NULL OR EXISTS (SELECT 1 FROM quantity_item x WHERE x.id = r.quantity_item_id))
   AND NOT EXISTS (SELECT 1 FROM reservation e WHERE e.id = r.id);

DELETE FROM migration_quarantine WHERE migration = '000194';
