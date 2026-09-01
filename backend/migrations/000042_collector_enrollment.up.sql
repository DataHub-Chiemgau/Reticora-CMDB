-- Migration 000042: collector enrollment codes (zero-config onboarding)
--
-- A collector enrolls by presenting a short-lived, single-use enrollment code
-- instead of a pre-shared credential (spec §8.1: Collector anschließen →
-- Infrastruktur erscheint). The code is stored as a SHA-256 hash so a database
-- read never discloses a usable code.
CREATE TABLE collector_enrollment_code (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL UNIQUE,
    label TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    used_by_collector_id UUID REFERENCES collector(id) ON DELETE SET NULL,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE collector_enrollment_code ENABLE ROW LEVEL SECURITY;
ALTER TABLE collector_enrollment_code FORCE ROW LEVEL SECURITY;
CREATE POLICY collector_enrollment_code_isolation ON collector_enrollment_code
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE INDEX idx_collector_enroll_org ON collector_enrollment_code(organization_id);
CREATE INDEX idx_collector_enroll_hash ON collector_enrollment_code(code_hash);
