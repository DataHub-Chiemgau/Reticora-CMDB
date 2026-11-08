-- WP-827 (ABN-03, SR-03, MGT-01): references of phase 2 that neither a
-- foreign key nor the write path kept in their organization or alive.
--
-- document_link names its target by entity_type and entity_id without a
-- foreign key (000011). Only a deleted contract removes its links
-- (delete_contract_document_links, 000238); a link to a CI removed by the
-- retention run, a deleted asset, ticket, location or stocktake stayed and
-- pointed at no row (tests/integrity-check.sql, orphan: document link
-- without target).
--
-- 1. delete_target_document_links removes the links of a deleted target of
--    the kind TG_ARGV[0] in the same transaction (AFTER DELETE triggers
--    <table>_document_links on ci, asset, ticket, location,
--    disposal_record, stocktake and stock_movement); the documents stay,
--    document.link_count follows via count_document_links. Like
--    delete_contract_document_links it runs with the scope widened
--    (app_scope_widen, 000217) and a fixed search_path. disposal_record,
--    stocktake and stock_movement are append-only; their trigger covers a
--    delete their guard admits.
-- 2. Links that already point at no row move to migration_quarantine
--    (reason 'dropped: link target missing') and are removed.
-- 3. reservation.assignee_id, ticket_id and order_id had no foreign key
--    (000055): POST /api/v1/reservations stored the id of a ticket
--    or order of another organization, or of none. They become composite
--    foreign keys with the organization (K19, pattern 000194), ON DELETE
--    SET NULL of the column; internal_order gets UNIQUE (id,
--    organization_id). An existing reference that names no row of the
--    organization is recorded in migration_quarantine with the whole row
--    and set to NULL (E-26). The repository answers the violation with 422
--    on the field (reservation.referenceFields).

-- ─── 1. Deletion ───────────────────────────────────────────────────────────────

CREATE FUNCTION delete_target_document_links() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    DELETE FROM document_link
     WHERE entity_type = TG_ARGV[0] AND entity_id = OLD.id AND organization_id = OLD.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER ci_document_links AFTER DELETE ON ci
    FOR EACH ROW EXECUTE FUNCTION delete_target_document_links('ci');
CREATE TRIGGER asset_document_links AFTER DELETE ON asset
    FOR EACH ROW EXECUTE FUNCTION delete_target_document_links('asset');
CREATE TRIGGER ticket_document_links AFTER DELETE ON ticket
    FOR EACH ROW EXECUTE FUNCTION delete_target_document_links('ticket');
CREATE TRIGGER location_document_links AFTER DELETE ON location
    FOR EACH ROW EXECUTE FUNCTION delete_target_document_links('location');
CREATE TRIGGER disposal_record_document_links AFTER DELETE ON disposal_record
    FOR EACH ROW EXECUTE FUNCTION delete_target_document_links('disposal_record');
CREATE TRIGGER stocktake_document_links AFTER DELETE ON stocktake
    FOR EACH ROW EXECUTE FUNCTION delete_target_document_links('stocktake');
CREATE TRIGGER stock_movement_document_links AFTER DELETE ON stock_movement
    FOR EACH ROW EXECUTE FUNCTION delete_target_document_links('stock_movement');

-- ─── 2. Existing orphans ───────────────────────────────────────────────────────

WITH orphan AS (
    SELECT l.id FROM document_link l
     WHERE NOT EXISTS (SELECT 1 FROM ci x WHERE l.entity_type = 'ci' AND x.id = l.entity_id)
       AND NOT EXISTS (SELECT 1 FROM asset x WHERE l.entity_type = 'asset' AND x.id = l.entity_id)
       AND NOT EXISTS (SELECT 1 FROM ticket x WHERE l.entity_type = 'ticket' AND x.id = l.entity_id)
       AND NOT EXISTS (SELECT 1 FROM location x WHERE l.entity_type = 'location' AND x.id = l.entity_id)
       AND NOT EXISTS (SELECT 1 FROM disposal_record x WHERE l.entity_type = 'disposal_record' AND x.id = l.entity_id)
       AND NOT EXISTS (SELECT 1 FROM stocktake x WHERE l.entity_type = 'stocktake' AND x.id = l.entity_id)
       AND NOT EXISTS (SELECT 1 FROM contract x WHERE l.entity_type = 'contract' AND x.id = l.entity_id)
       AND NOT EXISTS (SELECT 1 FROM stock_movement x WHERE l.entity_type = 'stock_movement' AND x.id = l.entity_id)
),
quarantined AS (
    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
    SELECT l.organization_id, 'document_link', to_jsonb(l), 'dropped: link target missing', '000275'
      FROM document_link l
      JOIN orphan o ON o.id = l.id
     ORDER BY l.id
)
DELETE FROM document_link l USING orphan o WHERE o.id = l.id;

-- ─── 3. References of reservation ──────────────────────────────────────────────

ALTER TABLE internal_order ADD CONSTRAINT internal_order_id_organization_key UNIQUE (id, organization_id);

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'reservation', to_jsonb(t),
       'SR-03 (WP-827): reservation.' || c.col || ' references no row of the organization, set to NULL', '000275'
  FROM reservation t
 CROSS JOIN LATERAL (VALUES
        ('assignee_id', t.assignee_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM app_user x WHERE x.id = t.assignee_id AND x.organization_id = t.organization_id)),
        ('ticket_id', t.ticket_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM ticket x WHERE x.id = t.ticket_id AND x.organization_id = t.organization_id)),
        ('order_id', t.order_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM internal_order x WHERE x.id = t.order_id AND x.organization_id = t.organization_id))
       ) AS c(col, crossing)
 WHERE c.crossing
 ORDER BY t.id, c.col;
UPDATE reservation t SET assignee_id = NULL
 WHERE t.assignee_id IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM app_user x WHERE x.id = t.assignee_id AND x.organization_id = t.organization_id);
UPDATE reservation t SET ticket_id = NULL
 WHERE t.ticket_id IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM ticket x WHERE x.id = t.ticket_id AND x.organization_id = t.organization_id);
UPDATE reservation t SET order_id = NULL
 WHERE t.order_id IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM internal_order x WHERE x.id = t.order_id AND x.organization_id = t.organization_id);

ALTER TABLE reservation
    ADD CONSTRAINT reservation_assignee_id_fkey FOREIGN KEY (assignee_id, organization_id)
        REFERENCES app_user (id, organization_id) ON DELETE SET NULL (assignee_id),
    ADD CONSTRAINT reservation_ticket_id_fkey FOREIGN KEY (ticket_id, organization_id)
        REFERENCES ticket (id, organization_id) ON DELETE SET NULL (ticket_id),
    ADD CONSTRAINT reservation_order_id_fkey FOREIGN KEY (order_id, organization_id)
        REFERENCES internal_order (id, organization_id) ON DELETE SET NULL (order_id);
