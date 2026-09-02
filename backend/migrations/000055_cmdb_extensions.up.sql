-- Migration 000055: enterprise CMDB + asset/inventory extension (additive).
--
-- Extends the metadata-driven ci_type model (custom types, field definitions
-- with validation/conditional rules, three attribute scopes), introduces
-- relationship type metadata with provenance, a configurable lifecycle engine,
-- the generalized location_node model, the unified stock movement ledger,
-- reservations, parent-asset/child-CI composition, field provenance with
-- manual overrides, unified entity change history, and saved views.
--
-- Conventions: UUID PKs, organization_id + RLS (app.org_id) with FORCE ROW
-- LEVEL SECURITY and WITH CHECK, JSONB for flexible metadata, permission
-- catalogue inserts with standard-role grants. Existing tables keep their
-- shape; hardcoded vocab CHECK constraints are relaxed in favor of
-- metadata-driven validation in the domain services.

-- ─── 1. Field types: relax the hardcoded data_type vocabulary ─────────────────
-- Validation moves to the field-definition registry in internal/fieldmeta.

ALTER TABLE ci_type_attribute DROP CONSTRAINT IF EXISTS ci_type_attribute_data_type_check;

-- ─── 2. Flexible CI type system ───────────────────────────────────────────────

ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS category TEXT;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS cloned_from_id UUID REFERENCES ci_type(id) ON DELETE SET NULL;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS capabilities JSONB NOT NULL DEFAULT '{}';
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS allowed_relationship_types JSONB NOT NULL DEFAULT '[]';
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS ui_schema JSONB NOT NULL DEFAULT '{}';
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS compliance_rules JSONB NOT NULL DEFAULT '[]';
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS discovery_mappings JSONB NOT NULL DEFAULT '[]';
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS is_logical BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS template_key TEXT;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS lifecycle_definition_id UUID;

CREATE INDEX IF NOT EXISTS idx_ci_type_org_active ON ci_type(organization_id, is_active);
CREATE INDEX IF NOT EXISTS idx_ci_type_template ON ci_type(template_key) WHERE template_key IS NOT NULL;

-- Full field-definition record (scope: type-level and org-global attributes).
ALTER TABLE ci_type_attribute ADD COLUMN IF NOT EXISTS label TEXT;
ALTER TABLE ci_type_attribute ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE ci_type_attribute ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'type';
ALTER TABLE ci_type_attribute ADD COLUMN IF NOT EXISTS ui_group TEXT;
ALTER TABLE ci_type_attribute ADD COLUMN IF NOT EXISTS validation JSONB NOT NULL DEFAULT '{}';
ALTER TABLE ci_type_attribute ADD COLUMN IF NOT EXISTS conditional JSONB;
ALTER TABLE ci_type_attribute ADD COLUMN IF NOT EXISTS reference_target TEXT;

-- Scope constraint is metadata-driven but the two scopes are fixed.
DO $$ BEGIN
    ALTER TABLE ci_type_attribute ADD CONSTRAINT ci_type_attribute_scope_check
        CHECK (scope IN ('global', 'type'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Global attribute definitions apply to every CI and must be unique per org.
-- ci_type_attribute carries no organization_id of its own (scoped via the
-- parent ci_type), so the uniqueness helper expression joins the type table.
CREATE UNIQUE INDEX IF NOT EXISTS idx_ci_type_attribute_global
    ON ci_type_attribute(ci_type_id, name) WHERE scope = 'global';

-- ─── 3. CI-instance field definitions (third attribute scope) ────────────────

CREATE TABLE ci_instance_field_definition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    label TEXT,
    description TEXT,
    data_type TEXT NOT NULL,
    required BOOLEAN NOT NULL DEFAULT false,
    default_value TEXT,
    enum_values JSONB,
    ui_group TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    validation JSONB NOT NULL DEFAULT '{}',
    conditional JSONB,
    reference_target TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ci_id, name)
);

ALTER TABLE ci_instance_field_definition ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_instance_field_definition FORCE ROW LEVEL SECURITY;
CREATE POLICY ci_instance_field_definition_isolation ON ci_instance_field_definition
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX idx_ci_instance_field_def_ci ON ci_instance_field_definition(ci_id);

-- ─── 4. Relationship type metadata ───────────────────────────────────────────

CREATE TABLE relationship_type (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID REFERENCES organization(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    forward_label TEXT NOT NULL,
    reverse_label TEXT NOT NULL,
    source_ci_types JSONB NOT NULL DEFAULT '[]',
    target_ci_types JSONB NOT NULL DEFAULT '[]',
    direction TEXT NOT NULL DEFAULT 'directed' CHECK (direction IN ('directed', 'undirected')),
    cardinality TEXT NOT NULL DEFAULT 'many_to_many',
    category TEXT,
    impact_participation BOOLEAN NOT NULL DEFAULT true,
    is_system BOOLEAN NOT NULL DEFAULT false,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT relationship_type_org_or_global CHECK (
        (organization_id IS NULL AND is_system) OR organization_id IS NOT NULL
    )
);

CREATE UNIQUE INDEX idx_relationship_type_org_key
    ON relationship_type(COALESCE(organization_id, '00000000-0000-0000-0000-000000000000'::uuid), key);

ALTER TABLE relationship_type ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_type FORCE ROW LEVEL SECURITY;
CREATE POLICY relationship_type_isolation ON relationship_type
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Seed the standard relationship type catalogue (global system types).
INSERT INTO relationship_type (organization_id, key, forward_label, reverse_label, category, is_system) VALUES
    (NULL, 'connected_to', 'connected to', 'connected to', 'network', true),
    (NULL, 'hosted_on', 'hosted on', 'hosts', 'infrastructure', true),
    (NULL, 'runs_on', 'runs on', 'hosts', 'infrastructure', true),
    (NULL, 'depends_on', 'depends on', 'required by', 'dependency', true),
    (NULL, 'member_of', 'member of', 'has member', 'grouping', true),
    (NULL, 'member_of_cluster', 'member of cluster', 'has cluster member', 'grouping', true),
    (NULL, 'powers', 'powers', 'powered by', 'power', true),
    (NULL, 'powered_by', 'powered by', 'powers', 'power', true),
    (NULL, 'mounted_in', 'mounted in', 'mounts', 'infrastructure', true),
    (NULL, 'uplink_to', 'uplink to', 'uplink from', 'network', true),
    (NULL, 'stores', 'stores', 'stored on', 'storage', true),
    (NULL, 'monitors', 'monitors', 'monitored by', 'monitoring', true),
    (NULL, 'backs_up', 'backs up', 'backed up by', 'backup', true),
    (NULL, 'backed_up_by', 'backed up by', 'backs up', 'backup', true),
    (NULL, 'managed_by', 'managed by', 'manages', 'management', true),
    (NULL, 'manages', 'manages', 'managed by', 'management', true),
    (NULL, 'assigned_to', 'assigned to', 'assigned', 'inventory', true),
    (NULL, 'contains', 'contains', 'contained by', 'composition', true),
    (NULL, 'contained_by', 'contained by', 'contains', 'composition', true),
    (NULL, 'parent_of', 'parent of', 'child of', 'composition', true),
    (NULL, 'child_of', 'child of', 'parent of', 'composition', true),
    (NULL, 'located_in', 'located in', 'contains', 'location', true),
    (NULL, 'uses', 'uses', 'used by', 'dependency', true),
    (NULL, 'used_by', 'used by', 'uses', 'dependency', true)
ON CONFLICT DO NOTHING;

-- ci_relationship: provenance + verification metadata, free rel_type vocabulary
-- validated against relationship_type in the domain layer.
ALTER TABLE ci_relationship DROP CONSTRAINT IF EXISTS ci_relationship_rel_type_check;
ALTER TABLE ci_relationship ADD COLUMN IF NOT EXISTS confidence NUMERIC(4,3);
ALTER TABLE ci_relationship ADD COLUMN IF NOT EXISTS first_seen_at TIMESTAMPTZ;
ALTER TABLE ci_relationship ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;
ALTER TABLE ci_relationship ADD COLUMN IF NOT EXISTS verification_state TEXT NOT NULL DEFAULT 'unverified';
ALTER TABLE ci_relationship ADD COLUMN IF NOT EXISTS source_system TEXT;
ALTER TABLE ci_relationship ADD COLUMN IF NOT EXISTS notes TEXT;

-- ─── 5. Lifecycle engine ─────────────────────────────────────────────────────

CREATE TABLE lifecycle_definition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID REFERENCES organization(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    name TEXT NOT NULL,
    applies_to TEXT NOT NULL DEFAULT 'asset' CHECK (applies_to IN ('asset', 'ci', 'both')),
    is_system BOOLEAN NOT NULL DEFAULT false,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT lifecycle_definition_org_or_global CHECK (
        (organization_id IS NULL AND is_system) OR organization_id IS NOT NULL
    )
);

CREATE UNIQUE INDEX idx_lifecycle_definition_org_key
    ON lifecycle_definition(COALESCE(organization_id, '00000000-0000-0000-0000-000000000000'::uuid), key);

CREATE TABLE lifecycle_state (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    definition_id UUID NOT NULL REFERENCES lifecycle_definition(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    label TEXT NOT NULL,
    is_initial BOOLEAN NOT NULL DEFAULT false,
    is_terminal BOOLEAN NOT NULL DEFAULT false,
    sort_order INTEGER NOT NULL DEFAULT 0,
    required_fields JSONB NOT NULL DEFAULT '[]',
    UNIQUE (definition_id, key)
);

CREATE TABLE lifecycle_transition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    definition_id UUID NOT NULL REFERENCES lifecycle_definition(id) ON DELETE CASCADE,
    from_state_id UUID REFERENCES lifecycle_state(id) ON DELETE CASCADE,
    to_state_id UUID NOT NULL REFERENCES lifecycle_state(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    label TEXT NOT NULL,
    required_fields JSONB NOT NULL DEFAULT '[]',
    validation JSONB NOT NULL DEFAULT '{}',
    UNIQUE (definition_id, from_state_id, to_state_id)
);

ALTER TABLE lifecycle_definition ENABLE ROW LEVEL SECURITY;
ALTER TABLE lifecycle_definition FORCE ROW LEVEL SECURITY;
CREATE POLICY lifecycle_definition_isolation ON lifecycle_definition
    USING (organization_id IS NULL OR organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE lifecycle_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE lifecycle_state FORCE ROW LEVEL SECURITY;
CREATE POLICY lifecycle_state_isolation ON lifecycle_state
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE lifecycle_transition ENABLE ROW LEVEL SECURITY;
ALTER TABLE lifecycle_transition FORCE ROW LEVEL SECURITY;
CREATE POLICY lifecycle_transition_isolation ON lifecycle_transition
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Seed the default physical asset lifecycle (spec §8).
INSERT INTO lifecycle_definition (id, organization_id, key, name, applies_to, is_system)
VALUES ('00000000-0000-0000-0000-000000000101', NULL, 'physical_asset', 'Physical Asset Lifecycle', 'asset', true)
ON CONFLICT DO NOTHING;

INSERT INTO lifecycle_state (organization_id, definition_id, key, label, is_initial, is_terminal, sort_order, required_fields) VALUES
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'ordered', 'Ordered', true, false, 10, '[]'),
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'received', 'Received', false, false, 20, '[]'),
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'in_stock', 'In Stock', false, false, 30, '["storage_location"]'),
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'reserved', 'Reserved', false, false, 40, '[]'),
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'preparing', 'Preparing', false, false, 50, '[]'),
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'deployed', 'Deployed', false, false, 60, '["deployment_location"]'),
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'repair', 'Repair', false, false, 70, '[]'),
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'retired', 'Retired', false, false, 80, '[]'),
    ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000101', 'disposed', 'Disposed', false, true, 90, '["disposal_date", "disposal_record"]')
ON CONFLICT DO NOTHING;

-- The system org placeholder keeps NOT NULL constraints satisfied for global rows;
-- RLS on lifecycle_state/transition still scopes by organization_id, and global
-- rows are exposed through the definition join in the repository.

-- Default transitions of the physical asset lifecycle.
INSERT INTO lifecycle_transition (organization_id, definition_id, from_state_id, to_state_id, key, label)
SELECT '00000000-0000-0000-0000-000000000001',
       s.definition_id, sfrom.id, sto.id,
       sfrom.key || '_to_' || sto.key,
       sfrom.label || ' → ' || sto.label
FROM (SELECT DISTINCT definition_id FROM lifecycle_state
      WHERE definition_id = '00000000-0000-0000-0000-000000000101') s
JOIN (SELECT id, key, label FROM lifecycle_state
      WHERE definition_id = '00000000-0000-0000-0000-000000000101') sfrom ON true
JOIN (SELECT id, key, label FROM lifecycle_state
      WHERE definition_id = '00000000-0000-0000-0000-000000000101') sto ON true
WHERE (sfrom.key, sto.key) IN (
    ('ordered', 'received'), ('received', 'in_stock'), ('in_stock', 'reserved'),
    ('reserved', 'preparing'), ('reserved', 'in_stock'), ('preparing', 'deployed'),
    ('deployed', 'repair'), ('deployed', 'in_stock'), ('repair', 'in_stock'),
    ('repair', 'disposed'), ('in_stock', 'retired'), ('deployed', 'retired'),
    ('retired', 'disposed'), ('retired', 'in_stock'), ('in_stock', 'deployed')
)
ON CONFLICT DO NOTHING;

-- Lifecycle state is tracked separately from technical health (ci.status and
-- monitoring keep their meaning). asset.status keeps the legacy vocabulary;
-- the new lifecycle columns are the metadata-driven state going forward.
ALTER TABLE ci ADD COLUMN IF NOT EXISTS lifecycle_state TEXT;
ALTER TABLE asset ADD COLUMN IF NOT EXISTS lifecycle_state TEXT;
ALTER TABLE asset DROP CONSTRAINT IF EXISTS asset_status_check;

-- Wire the type-level default lifecycle.
DO $$ BEGIN
    ALTER TABLE ci_type
        ADD CONSTRAINT ci_type_lifecycle_definition_fk
        FOREIGN KEY (lifecycle_definition_id) REFERENCES lifecycle_definition(id) ON DELETE SET NULL;
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- ─── 6. Generalized location model ───────────────────────────────────────────

CREATE TABLE location_node (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID REFERENCES client(id) ON DELETE SET NULL,
    parent_id UUID REFERENCES location_node(id) ON DELETE CASCADE,
    node_type TEXT NOT NULL,
    name TEXT NOT NULL,
    -- Optional back-references into the existing site/building/room/rack chain
    -- so rack visualization and room layouts keep working with one model.
    site_id UUID REFERENCES site(id) ON DELETE SET NULL,
    building_id UUID REFERENCES building(id) ON DELETE SET NULL,
    room_id UUID REFERENCES room(id) ON DELETE SET NULL,
    rack_id UUID REFERENCES rack(id) ON DELETE SET NULL,
    barcode TEXT,
    attributes JSONB NOT NULL DEFAULT '{}',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE location_node ENABLE ROW LEVEL SECURITY;
ALTER TABLE location_node FORCE ROW LEVEL SECURITY;
CREATE POLICY location_node_isolation ON location_node
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX idx_location_node_org ON location_node(organization_id);
CREATE INDEX idx_location_node_parent ON location_node(parent_id);
CREATE INDEX idx_location_node_type ON location_node(organization_id, node_type);
CREATE INDEX idx_location_node_client ON location_node(client_id) WHERE client_id IS NOT NULL;

-- Current location of CIs and assets (kept alongside the existing
-- site_id/room_id columns, which remain authoritative until migrated).
ALTER TABLE ci ADD COLUMN IF NOT EXISTS location_id UUID REFERENCES location_node(id) ON DELETE SET NULL;
ALTER TABLE asset ADD COLUMN IF NOT EXISTS location_id UUID REFERENCES location_node(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_ci_location ON ci(location_id) WHERE location_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_asset_location ON asset(location_id) WHERE location_id IS NOT NULL;

-- ─── 7. Unified stock movement ledger (append-only) ──────────────────────────
-- The existing consumable-scoped stock_movement (migration 000045) stays
-- untouched; asset_movement is the generalized ledger for serialized assets
-- and quantity items.

CREATE TABLE asset_movement (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    item_kind TEXT NOT NULL DEFAULT 'asset' CHECK (item_kind IN ('asset', 'quantity_item')),
    asset_id UUID REFERENCES asset(id) ON DELETE CASCADE,
    quantity_item_id UUID,
    movement_type TEXT NOT NULL,
    from_location_id UUID REFERENCES location_node(id) ON DELETE SET NULL,
    to_location_id UUID REFERENCES location_node(id) ON DELETE SET NULL,
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

-- Movement history is never overwritten.
CREATE OR REPLACE FUNCTION reject_asset_movement_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'asset_movement rows are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_asset_movement_no_update BEFORE UPDATE ON asset_movement
    FOR EACH ROW EXECUTE FUNCTION reject_asset_movement_mutation();
CREATE TRIGGER trg_asset_movement_no_delete BEFORE DELETE ON asset_movement
    FOR EACH ROW EXECUTE FUNCTION reject_asset_movement_mutation();

-- Quantity-based inventory items (cables, adapters, consumables): tracked by
-- quantity, separate from serialized CI identity. The existing consumable
-- table remains for the consumable module; quantity_item is the generalized
-- inventory item usable by the movement/reservation ledger.
CREATE TABLE quantity_item (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID REFERENCES client(id) ON DELETE SET NULL,
    sku TEXT,
    name TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'general',
    unit TEXT NOT NULL DEFAULT 'pcs',
    stock_level NUMERIC(12,2) NOT NULL DEFAULT 0,
    min_level NUMERIC(12,2) NOT NULL DEFAULT 0,
    location_id UUID REFERENCES location_node(id) ON DELETE SET NULL,
    notes TEXT,
    attributes JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, sku)
);

ALTER TABLE quantity_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE quantity_item FORCE ROW LEVEL SECURITY;
CREATE POLICY quantity_item_isolation ON quantity_item
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DO $$ BEGIN
    ALTER TABLE asset_movement
        ADD CONSTRAINT asset_movement_quantity_item_fk
        FOREIGN KEY (quantity_item_id) REFERENCES quantity_item(id) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- ─── 8. Reservations ─────────────────────────────────────────────────────────

CREATE TABLE reservation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    item_kind TEXT NOT NULL DEFAULT 'asset' CHECK (item_kind IN ('asset', 'quantity_item')),
    asset_id UUID REFERENCES asset(id) ON DELETE CASCADE,
    quantity_item_id UUID REFERENCES quantity_item(id) ON DELETE CASCADE,
    quantity NUMERIC(12,2) NOT NULL DEFAULT 1 CHECK (quantity > 0),
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'released', 'expired', 'consumed')),
    reserved_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    reserved_until TIMESTAMPTZ,
    assignee_id UUID,
    project_ref TEXT,
    order_id UUID,
    ticket_id UUID,
    expires_at TIMESTAMPTZ,
    reason TEXT,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT reservation_item_check CHECK (asset_id IS NOT NULL OR quantity_item_id IS NOT NULL)
);

ALTER TABLE reservation ENABLE ROW LEVEL SECURITY;
ALTER TABLE reservation FORCE ROW LEVEL SECURITY;
CREATE POLICY reservation_isolation ON reservation
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX idx_reservation_org_state ON reservation(organization_id, state);
CREATE INDEX idx_reservation_asset ON reservation(asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX idx_reservation_expiry ON reservation(expires_at) WHERE state = 'active' AND expires_at IS NOT NULL;

-- Serialized assets admit at most one active reservation at a time.
CREATE UNIQUE INDEX idx_reservation_one_active_per_asset ON reservation(asset_id)
    WHERE asset_id IS NOT NULL AND state = 'active';

-- ─── 9. Parent Asset / Child CI composition ──────────────────────────────────

ALTER TABLE asset ADD COLUMN IF NOT EXISTS asset_type_id UUID REFERENCES ci_type(id) ON DELETE SET NULL;
ALTER TABLE asset ADD COLUMN IF NOT EXISTS parent_asset_id UUID REFERENCES asset(id) ON DELETE SET NULL;

-- CI ↔ Asset is an optional one-to-one association.
CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_ci_unique ON asset(ci_id) WHERE ci_id IS NOT NULL;

CREATE TABLE composition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    parent_asset_id UUID NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
    child_ci_id UUID REFERENCES ci(id) ON DELETE CASCADE,
    child_asset_id UUID REFERENCES asset(id) ON DELETE CASCADE,
    role TEXT,
    position TEXT,
    configuration_only BOOLEAN NOT NULL DEFAULT true,
    independently_serialized BOOLEAN NOT NULL DEFAULT false,
    independently_assignable BOOLEAN NOT NULL DEFAULT false,
    independently_locatable BOOLEAN NOT NULL DEFAULT false,
    independently_lifecycle_managed BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT composition_child_check CHECK (
        (child_ci_id IS NOT NULL AND child_asset_id IS NULL) OR
        (child_ci_id IS NULL AND child_asset_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX idx_composition_unique_child_ci ON composition(parent_asset_id, child_ci_id) WHERE child_ci_id IS NOT NULL;
CREATE UNIQUE INDEX idx_composition_unique_child_asset ON composition(parent_asset_id, child_asset_id) WHERE child_asset_id IS NOT NULL;
CREATE INDEX idx_composition_parent ON composition(parent_asset_id);

ALTER TABLE composition ENABLE ROW LEVEL SECURITY;
ALTER TABLE composition FORCE ROW LEVEL SECURITY;
CREATE POLICY composition_isolation ON composition
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- ─── 10. Field provenance & manual overrides ─────────────────────────────────

CREATE TABLE ci_field_value (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    field_name TEXT NOT NULL,
    discovered_value JSONB,
    discovered_source TEXT,
    discovered_at TIMESTAMPTZ,
    override_value JSONB,
    override_author UUID,
    override_reason TEXT,
    override_at TIMESTAMPTZ,
    protected BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ci_id, field_name)
);

ALTER TABLE ci_field_value ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_field_value FORCE ROW LEVEL SECURITY;
CREATE POLICY ci_field_value_isolation ON ci_field_value
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX idx_ci_field_value_ci ON ci_field_value(ci_id);

-- Per-organization source-priority policy for effective-value resolution.
CREATE TABLE source_priority_policy (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT 'default',
    -- Ordered list of sources, highest priority first. The pseudo-source
    -- 'manual_override' participates so tenants can decide whether protected
    -- overrides always win (default) or trusted discovery may supersede them.
    priorities JSONB NOT NULL DEFAULT
        '["manual_override", "ipmi", "redfish", "api", "agent", "wmi", "ssh", "snmp", "sweep", "manual", "import"]',
    is_default BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

ALTER TABLE source_priority_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE source_priority_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY source_priority_policy_isolation ON source_priority_policy
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- ─── 11. Unified entity change history ───────────────────────────────────────
-- ci_change keeps tracking CI mutations; entity_change generalizes the same
-- shape for assets and other inventory subjects.

CREATE TABLE entity_change (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    entity_type TEXT NOT NULL,
    entity_id UUID NOT NULL,
    actor_id UUID,
    change_type TEXT NOT NULL,
    field_name TEXT,
    old_value JSONB,
    new_value JSONB,
    comment TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE entity_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_change FORCE ROW LEVEL SECURITY;
CREATE POLICY entity_change_isolation ON entity_change
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX idx_entity_change_entity ON entity_change(entity_type, entity_id, created_at DESC);
CREATE INDEX idx_entity_change_org ON entity_change(organization_id, created_at DESC);

-- Broaden the ci_change vocabulary for the new change classes.
ALTER TABLE ci_change DROP CONSTRAINT IF EXISTS chk_ci_change_type;
ALTER TABLE ci_change ADD CONSTRAINT chk_ci_change_type CHECK (change_type IN (
    'create', 'update', 'delete', 'status_change', 'relationship_change',
    'attribute_change', 'type_change', 'lifecycle_transition',
    'location_change', 'movement', 'override', 'reconciliation_decision',
    'parent_child_change', 'assignment'
));

-- ─── 12. Saved views ─────────────────────────────────────────────────────────

CREATE TABLE saved_view (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    owner_id UUID,
    name TEXT NOT NULL,
    entity_kind TEXT NOT NULL DEFAULT 'ci',
    filter_spec JSONB NOT NULL DEFAULT '{}',
    shared BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, owner_id, name)
);

ALTER TABLE saved_view ENABLE ROW LEVEL SECURITY;
ALTER TABLE saved_view FORCE ROW LEVEL SECURITY;
CREATE POLICY saved_view_isolation ON saved_view
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- ─── 13. updated_at triggers for the new mutable tables ──────────────────────

CREATE TRIGGER trg_ci_instance_field_definition_updated_at BEFORE UPDATE ON ci_instance_field_definition
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_relationship_type_updated_at BEFORE UPDATE ON relationship_type
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_lifecycle_definition_updated_at BEFORE UPDATE ON lifecycle_definition
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_location_node_updated_at BEFORE UPDATE ON location_node
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_quantity_item_updated_at BEFORE UPDATE ON quantity_item
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_reservation_updated_at BEFORE UPDATE ON reservation
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_composition_updated_at BEFORE UPDATE ON composition
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_ci_field_value_updated_at BEFORE UPDATE ON ci_field_value
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_source_priority_policy_updated_at BEFORE UPDATE ON source_priority_policy
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_saved_view_updated_at BEFORE UPDATE ON saved_view
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─── 14. Permission catalogue + standard-role grants (spec §21) ──────────────

INSERT INTO permission (key, resource, action, description) VALUES
    ('ci_type:manage', 'ci_type', 'manage', 'Manage CI types'),
    ('ci_attribute:manage', 'ci_attribute', 'manage', 'Manage CI attribute definitions'),
    ('ci_instance_attribute:manage', 'ci_instance_attribute', 'manage', 'Manage CI instance attributes'),
    ('relationship_type:manage', 'relationship_type', 'manage', 'Manage relationship types'),
    ('asset:assign', 'asset', 'assign', 'Assign assets to users or teams'),
    ('asset:move', 'asset', 'move', 'Move assets between locations'),
    ('asset:reserve', 'asset', 'reserve', 'Reserve assets and inventory items'),
    ('inventory:manage', 'inventory', 'manage', 'Manage inventory locations and stock'),
    ('lifecycle:manage', 'lifecycle', 'manage', 'Manage lifecycle definitions and transitions'),
    ('reconciliation:resolve', 'reconciliation', 'resolve', 'Resolve reconciliation conflicts'),
    ('override:write', 'override', 'write', 'Create and clear manual field overrides'),
    ('saved_view:read', 'saved_view', 'read', 'Read saved views'),
    ('saved_view:write', 'saved_view', 'write', 'Manage saved views')
ON CONFLICT (key) DO UPDATE SET
    resource = EXCLUDED.resource,
    action = EXCLUDED.action,
    description = EXCLUDED.description,
    updated_at = now();

-- org_admin receives every catalogue key by construction; grant the new keys
-- to engineer (write side) and viewer/client_technician (read side) to match
-- the existing role semantics.
INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES
    ('ci_type:manage'), ('ci_attribute:manage'), ('ci_instance_attribute:manage'),
    ('relationship_type:manage'), ('asset:assign'), ('asset:move'),
    ('asset:reserve'), ('inventory:manage'), ('lifecycle:manage'),
    ('reconciliation:resolve'), ('override:write'),
    ('saved_view:read'), ('saved_view:write')
) AS p(key)
WHERE r.name = 'org_admin'
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES
    ('asset:assign'), ('asset:move'), ('asset:reserve'),
    ('reconciliation:resolve'), ('override:write'),
    ('saved_view:read'), ('saved_view:write')
) AS p(key)
WHERE r.name = 'engineer'
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'saved_view:read'
FROM role r
WHERE r.name IN ('viewer', 'client_technician')
ON CONFLICT DO NOTHING;
