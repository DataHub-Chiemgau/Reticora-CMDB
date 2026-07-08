-- Phase 2: Document Management (Dokumentenverwaltung)
-- Stores document metadata; actual files reside in S3-compatible storage.

CREATE TABLE IF NOT EXISTS document (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    title           TEXT NOT NULL,
    description     TEXT,
    file_name       TEXT NOT NULL,
    file_size       BIGINT NOT NULL DEFAULT 0,
    mime_type       TEXT NOT NULL DEFAULT 'application/octet-stream',
    storage_key     TEXT NOT NULL,
    version         INT NOT NULL DEFAULT 1,
    category        TEXT NOT NULL DEFAULT 'general',
    tags            TEXT[] DEFAULT '{}',
    uploaded_by     UUID NOT NULL REFERENCES app_user(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT document_category_check CHECK (category IN (
        'general', 'contract', 'invoice', 'manual', 'diagram', 'certificate', 'policy', 'other'
    ))
);

CREATE INDEX idx_document_org ON document(organization_id);
CREATE INDEX idx_document_category ON document(organization_id, category);

-- Link documents to CIs, assets, or other entities
CREATE TABLE IF NOT EXISTS document_link (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id),
    document_id     UUID NOT NULL REFERENCES document(id) ON DELETE CASCADE,
    entity_type     TEXT NOT NULL,
    entity_id       UUID NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT doc_link_entity_type_check CHECK (entity_type IN (
        'ci', 'asset', 'ticket', 'assignment', 'stocktake'
    ))
);

CREATE INDEX idx_doc_link_doc ON document_link(document_id);
CREATE INDEX idx_doc_link_entity ON document_link(entity_type, entity_id);

ALTER TABLE document ENABLE ROW LEVEL SECURITY;
CREATE POLICY document_tenant_isolation ON document
    USING (organization_id = current_setting('app.current_org')::UUID);

ALTER TABLE document_link ENABLE ROW LEVEL SECURITY;
CREATE POLICY document_link_tenant_isolation ON document_link
    USING (organization_id = current_setting('app.current_org')::UUID);
