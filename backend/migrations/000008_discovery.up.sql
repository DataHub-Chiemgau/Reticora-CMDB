-- Phase 1: Discovery/Collector tables

CREATE TABLE collector (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id UUID REFERENCES client(id),
    name TEXT NOT NULL,
    version TEXT,
    status TEXT NOT NULL DEFAULT 'online' CHECK (status IN ('online', 'offline', 'degraded')),
    last_heartbeat TIMESTAMPTZ,
    config JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE discovery_job (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    collector_id UUID NOT NULL REFERENCES collector(id) ON DELETE CASCADE,
    job_type TEXT NOT NULL CHECK (job_type IN ('sweep', 'poll', 'full')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    config JSONB NOT NULL DEFAULT '{}', -- subnets, protocols, etc.
    result_summary JSONB,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE discovery_result (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    job_id UUID NOT NULL REFERENCES discovery_job(id) ON DELETE CASCADE,
    raw_data JSONB NOT NULL,
    fingerprint JSONB, -- identity keys for reconciliation
    matched_ci_id UUID REFERENCES ci(id),
    reconciliation_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (reconciliation_status IN ('pending', 'matched', 'created', 'conflict', 'ignored')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE collector ENABLE ROW LEVEL SECURITY;
ALTER TABLE discovery_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE discovery_result ENABLE ROW LEVEL SECURITY;

CREATE POLICY collector_isolation ON collector
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY discovery_job_isolation ON discovery_job
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE POLICY discovery_result_isolation ON discovery_result
    USING (organization_id = current_setting('app.organization_id')::UUID);

CREATE INDEX idx_collector_org ON collector(organization_id);
CREATE INDEX idx_discovery_job_collector ON discovery_job(collector_id);
CREATE INDEX idx_discovery_job_status ON discovery_job(status);
CREATE INDEX idx_discovery_result_job ON discovery_result(job_id);
CREATE INDEX idx_discovery_result_ci ON discovery_result(matched_ci_id) WHERE matched_ci_id IS NOT NULL;
