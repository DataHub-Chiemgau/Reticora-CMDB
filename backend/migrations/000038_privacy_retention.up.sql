-- Epic F: Data-retention policy per tenant (DSGVO Art. 5 Abs. 1 lit. e —
-- Speicherbegrenzung). Personal data older than the configured window can be
-- purged or anonymized by the erasure workflow.
CREATE TABLE IF NOT EXISTS privacy_retention_policy (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE UNIQUE,
    -- Retention window in days for personal data subjects (contacts,
    -- deactivated users). 0 means "keep forever" (no automatic erasure).
    retention_days INTEGER NOT NULL DEFAULT 0 CHECK (retention_days >= 0),
    -- What the erasure workflow does with records past the window:
    -- 'anonymize' keeps rows with surrogate values (referential integrity),
    -- 'delete' removes them outright.
    mode TEXT NOT NULL DEFAULT 'anonymize' CHECK (mode IN ('anonymize', 'delete')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE privacy_retention_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE privacy_retention_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY privacy_retention_isolation ON privacy_retention_policy
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

CREATE TRIGGER set_privacy_retention_policy_updated_at
    BEFORE UPDATE ON privacy_retention_policy
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
