-- Reverts 000246_drop_asset_movement.
--
-- 1. reject_asset_movement_mutation() and asset_movement come back in their
--    state after 000075 (RESTRICT on the location references) with ENABLE +
--    FORCE RLS, the policy, the append-only triggers and
--    reticora_truncate_guard (000144).
-- 2. The rows are filled from stock_movement (item_kind asset and
--    quantity_item) with the former type names (transfer →
--    warehouse_transfer, release → reservation_release, repair →
--    repair_transfer). Rows taken from assignment (notes 'assignment:<id>')
--    stay out: assignment still holds them. The rows 000242 quarantined come
--    back from migration_quarantine where their references still exist.

-- ─── 1. Table ──────────────────────────────────────────────────────────────────

CREATE FUNCTION reject_asset_movement_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'asset_movement rows are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TABLE asset_movement (
    id UUID CONSTRAINT asset_movement_pkey PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL CONSTRAINT asset_movement_organization_id_fkey
        REFERENCES organization(id) ON DELETE CASCADE,
    item_kind TEXT NOT NULL DEFAULT 'asset' CONSTRAINT asset_movement_item_kind_check
        CHECK (item_kind IN ('asset', 'quantity_item')),
    asset_id UUID CONSTRAINT asset_movement_asset_id_fkey REFERENCES asset(id) ON DELETE CASCADE,
    quantity_item_id UUID CONSTRAINT asset_movement_quantity_item_fk
        REFERENCES quantity_item(id) ON DELETE CASCADE,
    movement_type TEXT NOT NULL,
    from_location_id UUID CONSTRAINT asset_movement_from_location_id_fkey
        REFERENCES location(id) ON DELETE RESTRICT,
    to_location_id UUID CONSTRAINT asset_movement_to_location_id_fkey
        REFERENCES location(id) ON DELETE RESTRICT,
    quantity NUMERIC(12,2),
    actor_id UUID,
    reason TEXT,
    ticket_id UUID,
    order_id UUID,
    workflow_run_id UUID,
    document_id UUID,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT asset_movement_item_check CHECK (asset_id IS NOT NULL OR quantity_item_id IS NOT NULL)
);

ALTER TABLE asset_movement ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_movement FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_movement_isolation ON asset_movement
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX idx_asset_movement_org ON asset_movement(organization_id, created_at DESC);
CREATE INDEX idx_asset_movement_asset ON asset_movement(asset_id, created_at DESC) WHERE asset_id IS NOT NULL;
CREATE INDEX idx_asset_movement_type ON asset_movement(organization_id, movement_type);

CREATE TRIGGER trg_asset_movement_no_update BEFORE UPDATE ON asset_movement
    FOR EACH ROW EXECUTE FUNCTION reject_asset_movement_mutation();
CREATE TRIGGER trg_asset_movement_no_delete BEFORE DELETE ON asset_movement
    FOR EACH ROW EXECUTE FUNCTION reject_asset_movement_mutation();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON asset_movement
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 2. Rows ───────────────────────────────────────────────────────────────────

INSERT INTO asset_movement (
    id, organization_id, item_kind, asset_id, quantity_item_id, movement_type,
    from_location_id, to_location_id, quantity, actor_id, reason, ticket_id,
    order_id, workflow_run_id, document_id, notes, created_at)
SELECT m.id, m.organization_id, m.item_kind, m.asset_id, m.quantity_item_id,
       CASE m.movement_type
           WHEN 'transfer' THEN 'warehouse_transfer'
           WHEN 'release' THEN 'reservation_release'
           WHEN 'repair' THEN 'repair_transfer'
           ELSE m.movement_type
       END,
       m.from_location_id, m.to_location_id, m.quantity, m.actor_id, m.reason, m.ticket_id,
       m.order_id, m.workflow_run_id, m.document_id, m.notes, m.created_at
  FROM stock_movement m
 WHERE m.item_kind IN ('asset', 'quantity_item')
   AND (m.notes IS NULL OR m.notes !~ '^assignment:[0-9a-f-]{36}$');

INSERT INTO asset_movement
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::asset_movement, q.row_data) r
 WHERE q.migration = '000242' AND q.source_table = 'asset_movement'
   AND r.id IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM asset_movement a WHERE a.id = r.id)
   AND (r.asset_id IS NULL OR EXISTS (SELECT 1 FROM asset x WHERE x.id = r.asset_id))
   AND (r.quantity_item_id IS NULL OR EXISTS (SELECT 1 FROM quantity_item x WHERE x.id = r.quantity_item_id))
   AND (r.from_location_id IS NULL OR EXISTS (SELECT 1 FROM location x WHERE x.id = r.from_location_id))
   AND (r.to_location_id IS NULL OR EXISTS (SELECT 1 FROM location x WHERE x.id = r.to_location_id))
   AND EXISTS (SELECT 1 FROM organization x WHERE x.id = r.organization_id);
