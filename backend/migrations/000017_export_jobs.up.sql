-- Export job table
-- Corresponds to spec Migration 0011

CREATE TABLE export_job (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    initiated_by UUID NOT NULL,
    format TEXT NOT NULL DEFAULT 'csv',
    status TEXT NOT NULL DEFAULT 'pending',
    filters JSONB NOT NULL DEFAULT '{}',
    object_key TEXT,
    row_count INTEGER,
    file_size_bytes BIGINT,
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_export_format CHECK (format IN ('csv', 'json', 'xlsx')),
    CONSTRAINT chk_export_status CHECK (status IN ('pending', 'running', 'completed', 'failed', 'expired'))
);

-- RLS
ALTER TABLE export_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE export_job FORCE ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON export_job
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Indexes
CREATE INDEX idx_export_job_org ON export_job(organization_id);
CREATE INDEX idx_export_job_status ON export_job(organization_id, status);
