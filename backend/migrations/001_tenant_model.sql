-- Phase 0: Tenant model with Row-Level Security
-- Organizations (MSP/ISP mandants)

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE organization (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    plan TEXT NOT NULL DEFAULT 'essential',
    settings JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE client (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    settings JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, slug)
);

CREATE TABLE site (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID NOT NULL REFERENCES client(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    address TEXT,
    geo POINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE building (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    site_id UUID NOT NULL REFERENCES site(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    floors INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE room (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    building_id UUID NOT NULL REFERENCES building(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    floor INTEGER,
    room_type TEXT DEFAULT 'general',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE rack (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    room_id UUID NOT NULL REFERENCES room(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    height_u INTEGER NOT NULL DEFAULT 42,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Row-Level Security policies
ALTER TABLE organization ENABLE ROW LEVEL SECURITY;
ALTER TABLE client ENABLE ROW LEVEL SECURITY;
ALTER TABLE site ENABLE ROW LEVEL SECURITY;
ALTER TABLE building ENABLE ROW LEVEL SECURITY;
ALTER TABLE room ENABLE ROW LEVEL SECURITY;
ALTER TABLE rack ENABLE ROW LEVEL SECURITY;

-- RLS policies using session variable app.organization_id
CREATE POLICY org_isolation ON organization
    USING (id = current_setting('app.organization_id')::UUID);

CREATE POLICY client_isolation ON client
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY site_isolation ON site
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY building_isolation ON building
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY room_isolation ON room
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY rack_isolation ON rack
    USING (organization_id = current_setting('app.organization_id')::UUID);

-- Indexes
CREATE INDEX idx_client_org ON client(organization_id);
CREATE INDEX idx_site_org ON site(organization_id);
CREATE INDEX idx_site_client ON site(client_id);
CREATE INDEX idx_building_site ON building(site_id);
CREATE INDEX idx_room_building ON room(building_id);
CREATE INDEX idx_rack_room ON rack(room_id);
