-- Phase 0: Audit log with hash-chain integrity

CREATE TABLE audit_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_id UUID,
    actor_type TEXT NOT NULL DEFAULT 'user', -- user, system, collector
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id UUID,
    changes JSONB,
    previous_hash TEXT NOT NULL DEFAULT '',
    hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;

CREATE POLICY audit_isolation ON audit_log
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE INDEX idx_audit_org ON audit_log(organization_id);
CREATE INDEX idx_audit_timestamp ON audit_log(organization_id, timestamp DESC);
CREATE INDEX idx_audit_resource ON audit_log(resource_type, resource_id);
CREATE INDEX idx_audit_actor ON audit_log(actor_id);
