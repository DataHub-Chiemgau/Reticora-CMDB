-- Network interfaces, subnets, IP addresses, and cables
-- Corresponds to spec Migration 0004 (Netzwerk/IPAM/Kabel)

CREATE TABLE network_interface (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    mac_address MACADDR,
    interface_type TEXT NOT NULL DEFAULT 'ethernet',
    speed_mbps INTEGER,
    is_management BOOLEAN NOT NULL DEFAULT false,
    is_uplink BOOLEAN NOT NULL DEFAULT false,
    admin_status TEXT NOT NULL DEFAULT 'up',
    oper_status TEXT NOT NULL DEFAULT 'unknown',
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_interface_type CHECK (interface_type IN ('ethernet', 'fiber', 'wifi', 'virtual', 'loopback', 'serial', 'management'))
);

CREATE TABLE subnet (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID REFERENCES client(id) ON DELETE SET NULL,
    site_id UUID REFERENCES site(id) ON DELETE SET NULL,
    cidr CIDR NOT NULL,
    name TEXT,
    vlan_id INTEGER,
    gateway INET,
    dns_servers INET[],
    description TEXT,
    is_management BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, cidr)
);

CREATE TABLE ip_address (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    subnet_id UUID REFERENCES subnet(id) ON DELETE SET NULL,
    interface_id UUID REFERENCES network_interface(id) ON DELETE SET NULL,
    address INET NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    dns_name TEXT,
    description TEXT,
    last_seen_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_ip_status CHECK (status IN ('active', 'reserved', 'deprecated', 'dhcp', 'available')),
    UNIQUE (organization_id, address)
);

CREATE TABLE cable (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    label TEXT,
    cable_type TEXT NOT NULL DEFAULT 'copper',
    length_m NUMERIC(8,2),
    color TEXT,
    source_interface_id UUID REFERENCES network_interface(id) ON DELETE SET NULL,
    target_interface_id UUID REFERENCES network_interface(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'connected',
    installed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_cable_type CHECK (cable_type IN ('copper', 'fiber_sm', 'fiber_mm', 'coaxial', 'power', 'other')),
    CONSTRAINT chk_cable_status CHECK (status IN ('connected', 'planned', 'decommissioned'))
);

-- RLS
ALTER TABLE network_interface ENABLE ROW LEVEL SECURITY;
ALTER TABLE network_interface FORCE ROW LEVEL SECURITY;
ALTER TABLE subnet ENABLE ROW LEVEL SECURITY;
ALTER TABLE subnet FORCE ROW LEVEL SECURITY;
ALTER TABLE ip_address ENABLE ROW LEVEL SECURITY;
ALTER TABLE ip_address FORCE ROW LEVEL SECURITY;
ALTER TABLE cable ENABLE ROW LEVEL SECURITY;
ALTER TABLE cable FORCE ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON network_interface
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE POLICY org_isolation ON subnet
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE POLICY org_isolation ON ip_address
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE POLICY org_isolation ON cable
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Indexes
CREATE INDEX idx_network_interface_ci ON network_interface(ci_id);
CREATE INDEX idx_network_interface_org ON network_interface(organization_id);
CREATE INDEX idx_subnet_org ON subnet(organization_id);
CREATE INDEX idx_ip_address_org ON ip_address(organization_id);
CREATE INDEX idx_ip_address_subnet ON ip_address(subnet_id);
CREATE INDEX idx_cable_org ON cable(organization_id);
CREATE INDEX idx_cable_source ON cable(source_interface_id);
CREATE INDEX idx_cable_target ON cable(target_interface_id);
