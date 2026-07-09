-- CI change history tracking
-- Records every modification to a CI for audit and rollback purposes.

CREATE TABLE ci_change (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    actor_id UUID, -- references app_user(id) or service account; no FK to allow external identities
    change_type TEXT NOT NULL,
    field_name TEXT,
    old_value JSONB,
    new_value JSONB,
    comment TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_ci_change_type CHECK (change_type IN ('create', 'update', 'delete', 'status_change', 'relationship_change', 'attribute_change'))
);

-- RLS
ALTER TABLE ci_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_change FORCE ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON ci_change
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Indexes
CREATE INDEX idx_ci_change_ci ON ci_change(ci_id, created_at DESC);
CREATE INDEX idx_ci_change_org ON ci_change(organization_id);
CREATE INDEX idx_ci_change_actor ON ci_change(actor_id);
CREATE INDEX idx_ci_change_type ON ci_change(change_type);
