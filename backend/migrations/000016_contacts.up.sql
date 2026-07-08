-- Contacts and CI-Contact relationships
-- Corresponds to spec Migration 0006 additions

CREATE TABLE contact (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID REFERENCES client(id) ON DELETE SET NULL,
    display_name TEXT NOT NULL,
    email TEXT,
    phone TEXT,
    role TEXT,
    department TEXT,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ci_contact (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    contact_id UUID NOT NULL REFERENCES contact(id) ON DELETE CASCADE,
    relationship_type TEXT NOT NULL DEFAULT 'responsible',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_contact_rel_type CHECK (relationship_type IN ('responsible', 'owner', 'operator', 'vendor', 'escalation')),
    UNIQUE (ci_id, contact_id, relationship_type)
);

-- RLS
ALTER TABLE contact ENABLE ROW LEVEL SECURITY;
ALTER TABLE contact FORCE ROW LEVEL SECURITY;
ALTER TABLE ci_contact ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_contact FORCE ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON contact
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE POLICY org_isolation ON ci_contact
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Indexes
CREATE INDEX idx_contact_org ON contact(organization_id);
CREATE INDEX idx_ci_contact_ci ON ci_contact(ci_id);
CREATE INDEX idx_ci_contact_contact ON ci_contact(contact_id);
