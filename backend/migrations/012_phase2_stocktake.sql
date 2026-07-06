-- Phase 2: Stocktake / Inventur (Standard)
-- Supports periodic inventory counts with scan tracking.

CREATE TABLE IF NOT EXISTS stocktake (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    title           TEXT NOT NULL,
    description     TEXT,
    status          TEXT NOT NULL DEFAULT 'planned',
    scope           TEXT NOT NULL DEFAULT 'full',
    started_by      UUID REFERENCES app_user(id),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    due_date        DATE,
    total_expected  INT DEFAULT 0,
    total_scanned   INT DEFAULT 0,
    total_missing   INT DEFAULT 0,
    total_surplus   INT DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT stocktake_status_check CHECK (status IN (
        'planned', 'in_progress', 'completed', 'cancelled'
    )),
    CONSTRAINT stocktake_scope_check CHECK (scope IN (
        'full', 'partial', 'site', 'room', 'rack'
    ))
);

CREATE TABLE IF NOT EXISTS stock_scan (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    stocktake_id    UUID NOT NULL REFERENCES stocktake(id) ON DELETE CASCADE,
    asset_id        UUID REFERENCES asset(id),
    ci_id           UUID REFERENCES ci(id),
    scanned_by      UUID NOT NULL REFERENCES app_user(id),
    scan_method     TEXT NOT NULL DEFAULT 'manual',
    scan_result     TEXT NOT NULL DEFAULT 'found',
    location_found  TEXT,
    notes           TEXT,
    scanned_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT scan_method_check CHECK (scan_method IN (
        'manual', 'barcode', 'rfid', 'qrcode'
    )),
    CONSTRAINT scan_result_check CHECK (scan_result IN (
        'found', 'missing', 'surplus', 'damaged', 'wrong_location'
    ))
);

CREATE INDEX idx_stocktake_org ON stocktake(organization_id);
CREATE INDEX idx_stocktake_status ON stocktake(organization_id, status);
CREATE INDEX idx_stock_scan_stocktake ON stock_scan(stocktake_id);
CREATE INDEX idx_stock_scan_asset ON stock_scan(asset_id);

ALTER TABLE stocktake ENABLE ROW LEVEL SECURITY;
CREATE POLICY stocktake_tenant_isolation ON stocktake
    USING (organization_id = current_setting('app.current_org')::UUID);

ALTER TABLE stock_scan ENABLE ROW LEVEL SECURITY;
CREATE POLICY stock_scan_tenant_isolation ON stock_scan
    USING (organization_id = current_setting('app.current_org')::UUID);
