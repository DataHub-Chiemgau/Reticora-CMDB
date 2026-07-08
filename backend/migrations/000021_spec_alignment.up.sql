-- Migration 000021: Align CI table with specification
-- Adds missing columns: site_id, room_id, hardware_uuid, primary_mac, hostname,
-- fqdn, os_name, os_version, sys_object_id, discovery_source, first_seen_at,
-- last_seen_at, is_manual, deleted_at
-- Also adds missing rack columns: width_mm, depth_mm, notes
-- Also adds missing site columns: geo_lat, geo_lon, notes
-- Also adds missing building column: floorplan_object_key
-- Aligns ci_type with spec: key, display_name, attribute_schema, required_fields, is_system

-- ─── CI table additions ─────────────────────────────────────────────────────────
ALTER TABLE ci ADD COLUMN IF NOT EXISTS site_id UUID REFERENCES site(id);
ALTER TABLE ci ADD COLUMN IF NOT EXISTS room_id UUID REFERENCES room(id);
ALTER TABLE ci ADD COLUMN IF NOT EXISTS hardware_uuid UUID;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS primary_mac MACADDR;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS hostname TEXT;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS fqdn TEXT;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS os_name TEXT;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS os_version TEXT;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS sys_object_id TEXT;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS discovery_source TEXT
    CHECK (discovery_source IN ('snmp','ssh','redfish','ipmi','wmi','api','agent','sweep','manual'));
ALTER TABLE ci ADD COLUMN IF NOT EXISTS first_seen_at TIMESTAMPTZ;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS is_manual BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE ci ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

-- Update status CHECK to include 'unknown'
ALTER TABLE ci DROP CONSTRAINT IF EXISTS ci_status_check;
ALTER TABLE ci ADD CONSTRAINT ci_status_check
    CHECK (status IN ('active','inactive','maintenance','decommissioned','unknown'));

-- Additional indexes per spec
CREATE INDEX IF NOT EXISTS idx_ci_org_type ON ci(organization_id, ci_type_id);
CREATE INDEX IF NOT EXISTS idx_ci_org_status ON ci(organization_id, status);
CREATE INDEX IF NOT EXISTS idx_ci_hardware_uuid ON ci(hardware_uuid) WHERE hardware_uuid IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ci_primary_mac ON ci(primary_mac) WHERE primary_mac IS NOT NULL;

-- Full-text search index over name, hostname, manufacturer, model, serial_number
CREATE INDEX IF NOT EXISTS idx_ci_fts ON ci USING GIN (
    to_tsvector('simple',
        coalesce(name,'') || ' ' ||
        coalesce(hostname,'') || ' ' ||
        coalesce(manufacturer,'') || ' ' ||
        coalesce(model,'') || ' ' ||
        coalesce(serial_number,'')
    )
);

-- ─── Rack table additions ───────────────────────────────────────────────────────
ALTER TABLE rack ADD COLUMN IF NOT EXISTS width_mm INTEGER NOT NULL DEFAULT 600;
ALTER TABLE rack ADD COLUMN IF NOT EXISTS depth_mm INTEGER NOT NULL DEFAULT 1000;
ALTER TABLE rack ADD COLUMN IF NOT EXISTS notes TEXT;

-- Add height_u constraint (1-60)
ALTER TABLE rack DROP CONSTRAINT IF EXISTS rack_height_u_check;
ALTER TABLE rack ADD CONSTRAINT rack_height_u_check CHECK (height_u >= 1 AND height_u <= 60);

-- ─── Site table additions ───────────────────────────────────────────────────────
ALTER TABLE site ADD COLUMN IF NOT EXISTS geo_lat DOUBLE PRECISION;
ALTER TABLE site ADD COLUMN IF NOT EXISTS geo_lon DOUBLE PRECISION;
ALTER TABLE site ADD COLUMN IF NOT EXISTS notes TEXT;

-- Add unique name per org constraint if not exists
DO $$ BEGIN
    ALTER TABLE site ADD CONSTRAINT site_org_name_unique UNIQUE (organization_id, name);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- ─── Building table additions ───────────────────────────────────────────────────
ALTER TABLE building ADD COLUMN IF NOT EXISTS floorplan_object_key TEXT;

-- ─── CI Type alignment with spec ────────────────────────────────────────────────
-- The spec uses: key (unique per org), display_name, is_system, attribute_schema, required_fields
-- Current has: name, icon, is_builtin
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS key TEXT;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS display_name TEXT;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS attribute_schema JSONB;
ALTER TABLE ci_type ADD COLUMN IF NOT EXISTS required_fields TEXT[];

-- Allow organization_id to be NULL for global system types
ALTER TABLE ci_type ALTER COLUMN organization_id DROP NOT NULL;

-- Backfill key from name for existing records
UPDATE ci_type SET key = name WHERE key IS NULL;
UPDATE ci_type SET display_name = name WHERE display_name IS NULL;
UPDATE ci_type SET is_system = is_builtin;

-- ─── Seed system CI types (global, org_id NULL) ─────────────────────────────────
INSERT INTO ci_type (organization_id, key, name, display_name, is_system, is_builtin)
VALUES
    (NULL, 'switch', 'switch', 'Switch', true, true),
    (NULL, 'router', 'router', 'Router', true, true),
    (NULL, 'firewall', 'firewall', 'Firewall', true, true),
    (NULL, 'access_point', 'access_point', 'Access Point', true, true),
    (NULL, 'server', 'server', 'Server', true, true),
    (NULL, 'hypervisor', 'hypervisor', 'Hypervisor', true, true),
    (NULL, 'vm', 'vm', 'Virtual Machine', true, true),
    (NULL, 'client', 'client', 'Client', true, true),
    (NULL, 'pdu', 'pdu', 'PDU', true, true),
    (NULL, 'ups', 'ups', 'UPS', true, true),
    (NULL, 'nas', 'nas', 'NAS', true, true),
    (NULL, 'storage_array', 'storage_array', 'Storage Array', true, true),
    (NULL, 'printer', 'printer', 'Printer', true, true),
    (NULL, 'ip_phone', 'ip_phone', 'IP Phone', true, true),
    (NULL, 'camera', 'camera', 'Camera', true, true),
    (NULL, 'generic_device', 'generic_device', 'Generic Device', true, true),
    (NULL, 'patch_panel', 'patch_panel', 'Patch Panel', true, true)
ON CONFLICT DO NOTHING;

-- ─── Rack mount with GiST exclusion ────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS rack_mount (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    rack_id UUID NOT NULL REFERENCES rack(id) ON DELETE CASCADE,
    ci_id UUID NOT NULL UNIQUE REFERENCES ci(id) ON DELETE CASCADE,
    position_u INTEGER NOT NULL CHECK (position_u >= 1),
    height_u INTEGER NOT NULL DEFAULT 1 CHECK (height_u >= 1),
    face TEXT NOT NULL DEFAULT 'front' CHECK (face IN ('front','rear','both')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- GiST exclusion to prevent overlap in same rack+face
CREATE EXTENSION IF NOT EXISTS btree_gist;
ALTER TABLE rack_mount ADD CONSTRAINT rack_mount_no_overlap
    EXCLUDE USING gist (
        rack_id WITH =,
        face WITH =,
        int4range(position_u, position_u + height_u) WITH &&
    ) WHERE (face != 'both');

CREATE INDEX IF NOT EXISTS idx_rack_mount_rack ON rack_mount(rack_id);

-- RLS for rack_mount
ALTER TABLE rack_mount ENABLE ROW LEVEL SECURITY;
ALTER TABLE rack_mount FORCE ROW LEVEL SECURITY;
CREATE POLICY rack_mount_isolation ON rack_mount
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- ─── Relationship suppression table ────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS relationship_suppression (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    source_ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    target_ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    relationship_type_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, source_ci_id, target_ci_id, relationship_type_id)
);

ALTER TABLE relationship_suppression ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_suppression FORCE ROW LEVEL SECURITY;
CREATE POLICY suppression_isolation ON relationship_suppression
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- ─── Credential table (envelope encryption) ────────────────────────────────────
CREATE TABLE IF NOT EXISTS credential (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID REFERENCES client(id),
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('snmp_v2c','snmp_v3','ssh_password','ssh_key','redfish','ipmi','wmi','api_token','nut')),
    ciphertext BYTEA NOT NULL,
    key_version INTEGER NOT NULL DEFAULT 1,
    scope TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE credential ENABLE ROW LEVEL SECURITY;
ALTER TABLE credential FORCE ROW LEVEL SECURITY;
CREATE POLICY credential_isolation ON credential
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- ─── Org DEK for envelope encryption ───────────────────────────────────────────
CREATE TABLE IF NOT EXISTS org_dek (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE UNIQUE,
    encrypted_dek BYTEA NOT NULL,
    key_version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ─── Review items table ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS review_item (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('ambiguous_identity','conflicting_values','unclassified_device')),
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')),
    payload JSONB NOT NULL DEFAULT '{}',
    candidate_ci_ids UUID[],
    resolved_by UUID,
    resolved_at TIMESTAMPTZ,
    resolution TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE review_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE review_item FORCE ROW LEVEL SECURITY;
CREATE POLICY review_item_isolation ON review_item
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
