-- Phase 0: CI type system and core CI table

CREATE TABLE ci_type (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    icon TEXT,
    is_builtin BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE ci_type_attribute (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ci_type_id UUID NOT NULL REFERENCES ci_type(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    data_type TEXT NOT NULL CHECK (data_type IN ('string', 'number', 'boolean', 'date', 'enum')),
    required BOOLEAN NOT NULL DEFAULT false,
    default_value TEXT,
    enum_values JSONB,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ci_type_id, name)
);

CREATE TABLE ci (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID REFERENCES client(id),
    ci_type_id UUID NOT NULL REFERENCES ci_type(id),
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'maintenance', 'decommissioned')),
    -- Common typed columns for performance (hybrid approach)
    manufacturer TEXT,
    model TEXT,
    serial_number TEXT,
    management_ip INET,
    firmware_version TEXT,
    -- Flexible attributes
    attributes JSONB NOT NULL DEFAULT '{}',
    -- Discovery metadata
    source TEXT, -- 'discovery', 'agent', 'manual'
    last_seen TIMESTAMPTZ,
    -- Standard fields
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- RLS
ALTER TABLE ci_type ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci ENABLE ROW LEVEL SECURITY;

CREATE POLICY ci_type_isolation ON ci_type
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY ci_isolation ON ci
    USING (organization_id = current_setting('app.organization_id')::UUID);

-- Indexes
CREATE INDEX idx_ci_org ON ci(organization_id);
CREATE INDEX idx_ci_type ON ci(ci_type_id);
CREATE INDEX idx_ci_client ON ci(client_id);
CREATE INDEX idx_ci_status ON ci(status);
CREATE INDEX idx_ci_serial ON ci(serial_number) WHERE serial_number IS NOT NULL;
CREATE INDEX idx_ci_mgmt_ip ON ci(management_ip) WHERE management_ip IS NOT NULL;
CREATE INDEX idx_ci_last_seen ON ci(last_seen);
CREATE INDEX idx_ci_attributes ON ci USING GIN(attributes);
