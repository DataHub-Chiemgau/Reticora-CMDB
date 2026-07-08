-- Phase 1: CI Relationships (edges in the configuration graph)

CREATE TABLE ci_relationship (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    source_ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    target_ci_id UUID NOT NULL REFERENCES ci(id) ON DELETE CASCADE,
    rel_type TEXT NOT NULL CHECK (rel_type IN (
        'connected_to', 'hosted_on', 'depends_on', 'member_of',
        'powers', 'stores', 'monitors', 'backs_up'
    )),
    attributes JSONB NOT NULL DEFAULT '{}',
    source TEXT NOT NULL DEFAULT 'manual', -- manual, discovery, agent
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, source_ci_id, target_ci_id, rel_type)
);

ALTER TABLE ci_relationship ENABLE ROW LEVEL SECURITY;

CREATE POLICY ci_relationship_isolation ON ci_relationship
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE INDEX idx_ci_rel_org ON ci_relationship(organization_id);
CREATE INDEX idx_ci_rel_source ON ci_relationship(source_ci_id);
CREATE INDEX idx_ci_rel_target ON ci_relationship(target_ci_id);
CREATE INDEX idx_ci_rel_type ON ci_relationship(rel_type);
