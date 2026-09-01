-- Migration 000045: consumables / stock movements (Lagerverwaltung)
--
-- Verbrauchsmaterial with stock level, min-level alerting (spec §6.9). Stock
-- changes are tracked as movements so the level is auditable.
CREATE TABLE IF NOT EXISTS consumable (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id       UUID REFERENCES client(id),
    name            TEXT NOT NULL,
    sku             TEXT,
    category        TEXT NOT NULL DEFAULT 'general',
    unit            TEXT NOT NULL DEFAULT 'pcs',
    stock_level     NUMERIC(12,2) NOT NULL DEFAULT 0,
    min_level       NUMERIC(12,2) NOT NULL DEFAULT 0,
    location        TEXT,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, sku)
);

CREATE TABLE IF NOT EXISTS stock_movement (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    consumable_id   UUID NOT NULL REFERENCES consumable(id) ON DELETE CASCADE,
    direction       TEXT NOT NULL CHECK (direction IN ('in', 'out')),
    quantity        NUMERIC(12,2) NOT NULL CHECK (quantity > 0),
    reason          TEXT,
    reference       TEXT,
    actor_id        UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE consumable ENABLE ROW LEVEL SECURITY;
ALTER TABLE consumable FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS consumable_isolation ON consumable;
CREATE POLICY consumable_isolation ON consumable
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE stock_movement ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_movement FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS stock_movement_isolation ON stock_movement;
CREATE POLICY stock_movement_isolation ON stock_movement
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX IF NOT EXISTS idx_consumable_org ON consumable(organization_id);
CREATE INDEX IF NOT EXISTS idx_stock_movement_consumable ON stock_movement(consumable_id);

-- Permission catalogue entries for the consumable module; org_admin receives
-- them via the standard role seed (SELECT key FROM permission).
INSERT INTO permission (key, resource, action, description) VALUES
    ('consumable:read', 'consumable', 'read', 'Read consumable stock'),
    ('consumable:write', 'consumable', 'write', 'Manage consumable stock')
ON CONFLICT (key) DO NOTHING;

-- Grant to the standard roles that manage inventory (org_admin, engineer).
INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES ('consumable:read'), ('consumable:write')) AS p(key)
WHERE r.name IN ('org_admin', 'engineer')
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'consumable:read'
FROM role r
WHERE r.name IN ('viewer', 'client_technician')
ON CONFLICT DO NOTHING;
