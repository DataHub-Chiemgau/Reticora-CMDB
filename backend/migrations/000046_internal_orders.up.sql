-- Migration 000046: internal order system (internes Bestellsystem)
CREATE TABLE IF NOT EXISTS internal_order (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id       UUID REFERENCES client(id),
    order_number    TEXT NOT NULL,
    title           TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','submitted','approved','rejected','ordered','received','cancelled')),
    requested_by    UUID,
    approved_by     UUID,
    supplier        TEXT,
    total_cost      NUMERIC(12,2),
    currency        TEXT DEFAULT 'EUR',
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, order_number)
);

CREATE TABLE IF NOT EXISTS internal_order_item (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    order_id        UUID NOT NULL REFERENCES internal_order(id) ON DELETE CASCADE,
    description     TEXT NOT NULL,
    quantity        NUMERIC(12,2) NOT NULL CHECK (quantity > 0),
    unit_price      NUMERIC(12,2),
    consumable_id   UUID REFERENCES consumable(id) ON DELETE SET NULL,
    asset_id        UUID REFERENCES asset(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE internal_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE internal_order FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS internal_order_isolation ON internal_order;
CREATE POLICY internal_order_isolation ON internal_order
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

ALTER TABLE internal_order_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE internal_order_item FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS internal_order_item_isolation ON internal_order_item;
CREATE POLICY internal_order_item_isolation ON internal_order_item
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX IF NOT EXISTS idx_internal_order_org ON internal_order(organization_id);
CREATE INDEX IF NOT EXISTS idx_internal_order_item_order ON internal_order_item(order_id);

INSERT INTO permission (key, resource, action, description) VALUES
    ('order:read', 'order', 'read', 'Read internal orders'),
    ('order:write', 'order', 'write', 'Manage internal orders'),
    ('order:approve', 'order', 'approve', 'Approve internal orders')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES ('order:read'), ('order:write')) AS p(key)
WHERE r.name IN ('org_admin', 'engineer')
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'order:approve'
FROM role r
WHERE r.name = 'org_admin'
ON CONFLICT DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'order:read'
FROM role r
WHERE r.name IN ('viewer', 'client_technician')
ON CONFLICT DO NOTHING;
