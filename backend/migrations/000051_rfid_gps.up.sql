-- Migration 000051: RFID tags on assets + asset location history (GPS advanced)

-- RFID tag and barcode for direct-access labels and scan flows.
ALTER TABLE asset ADD COLUMN IF NOT EXISTS rfid_tag TEXT;
ALTER TABLE asset ADD COLUMN IF NOT EXISTS barcode TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_rfid ON asset(organization_id, rfid_tag) WHERE rfid_tag IS NOT NULL;

-- Asset location history: GPS track over time (spec §6.9 GPS(Advanced)).
CREATE TABLE IF NOT EXISTS asset_location (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    asset_id        UUID NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
    lat             DOUBLE PRECISION NOT NULL,
    lon             DOUBLE PRECISION NOT NULL,
    accuracy_m      DOUBLE PRECISION,
    source          TEXT NOT NULL DEFAULT 'scan',
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE asset_location ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_location FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS asset_location_isolation ON asset_location;
CREATE POLICY asset_location_isolation ON asset_location
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX IF NOT EXISTS idx_asset_location_asset ON asset_location(asset_id, recorded_at DESC);
