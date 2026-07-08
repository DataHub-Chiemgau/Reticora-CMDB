-- Phase 2: Asset / Cloud-Inventar
-- Tracks purchased/leased assets with lifecycle and financial data.

CREATE TABLE IF NOT EXISTS asset (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    client_id       UUID REFERENCES client(id),
    ci_id           UUID REFERENCES ci(id),
    asset_tag       TEXT NOT NULL,
    name            TEXT NOT NULL,
    category        TEXT NOT NULL DEFAULT 'hardware',
    status          TEXT NOT NULL DEFAULT 'in_stock',
    purchase_date   DATE,
    purchase_cost   NUMERIC(12,2),
    currency        TEXT DEFAULT 'EUR',
    warranty_end    DATE,
    supplier        TEXT,
    invoice_number  TEXT,
    serial_number   TEXT,
    location        TEXT,
    notes           TEXT,
    custom_fields   JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT asset_status_check CHECK (status IN (
        'in_stock', 'assigned', 'maintenance', 'retired', 'disposed', 'lost'
    )),
    CONSTRAINT asset_category_check CHECK (category IN (
        'hardware', 'software', 'license', 'cloud_resource', 'accessory', 'other'
    ))
);

CREATE INDEX idx_asset_org ON asset(organization_id);
CREATE INDEX idx_asset_status ON asset(organization_id, status);
CREATE UNIQUE INDEX idx_asset_tag_org ON asset(organization_id, asset_tag);

ALTER TABLE asset ENABLE ROW LEVEL SECURITY;
CREATE POLICY asset_tenant_isolation ON asset
    USING (organization_id = current_setting('app.current_org')::UUID);
