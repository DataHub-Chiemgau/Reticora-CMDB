-- Stage 6: tenant-scoped search index and governed AI assistant audit trail.

INSERT INTO permission (key, resource, action, description) VALUES
    ('search:write', 'search', 'write', 'Rebuild tenant search indexes'),
    ('ai:read', 'ai', 'read', 'Use the AI assistant')
ON CONFLICT (key) DO UPDATE SET resource = EXCLUDED.resource, action = EXCLUDED.action, description = EXCLUDED.description, updated_at = now();

CREATE TABLE IF NOT EXISTS search_document (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    entity_type     TEXT NOT NULL,
    entity_id       UUID NOT NULL,
    title           TEXT NOT NULL,
    summary         TEXT NOT NULL DEFAULT '',
    url             TEXT NOT NULL DEFAULT '',
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    search_vector   tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(summary,''))) STORED,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT search_document_entity_type_check CHECK (entity_type IN ('ci','asset','document','ticket','contact','compliance')),
    UNIQUE (organization_id, entity_type, entity_id)
);
CREATE INDEX IF NOT EXISTS idx_search_document_org_type ON search_document(organization_id, entity_type);
CREATE INDEX IF NOT EXISTS idx_search_document_fts ON search_document USING GIN (search_vector);

CREATE TABLE IF NOT EXISTS ai_conversation (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    user_id         UUID REFERENCES app_user(id) ON DELETE SET NULL,
    title           TEXT NOT NULL DEFAULT 'Neue Unterhaltung',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ai_conversation_user ON ai_conversation(organization_id, user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS ai_message (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    conversation_id   UUID NOT NULL REFERENCES ai_conversation(id) ON DELETE CASCADE,
    role              TEXT NOT NULL CHECK (role IN ('user','assistant','system')),
    content           TEXT NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    citations         JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ai_message_conversation ON ai_message(organization_id, conversation_id, created_at);

CREATE TABLE IF NOT EXISTS ai_chunk (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    entity_type     TEXT NOT NULL,
    entity_id       UUID NOT NULL,
    title           TEXT NOT NULL,
    content         TEXT NOT NULL,
    url             TEXT NOT NULL DEFAULT '',
    embedding       JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ai_chunk_entity_type_check CHECK (entity_type IN ('ci','asset','document','ticket','compliance')),
    UNIQUE (organization_id, entity_type, entity_id)
);
CREATE INDEX IF NOT EXISTS idx_ai_chunk_entity ON ai_chunk(organization_id, entity_type, entity_id);

ALTER TABLE search_document ENABLE ROW LEVEL SECURITY;
ALTER TABLE search_document FORCE ROW LEVEL SECURITY;
CREATE POLICY search_document_tenant_isolation ON search_document USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE ai_conversation ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_conversation FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_conversation_tenant_isolation ON ai_conversation USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE ai_message ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_message FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_message_tenant_isolation ON ai_message USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
ALTER TABLE ai_chunk ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_chunk FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_chunk_tenant_isolation ON ai_chunk USING (organization_id = current_setting('app.org_id')::UUID) WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
