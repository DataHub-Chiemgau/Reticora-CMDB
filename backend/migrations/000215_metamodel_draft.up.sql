-- Migration 000215: metamodel drafts (WP-260, MET-53, API-06, E-112).
--
-- 1. metamodel_draft is the not yet published working state of metamodel
--    changes of an organization. items holds the typed operations
--    ({id, kind, op, target_id, target_key, payload, base,
--    schema_versions, route, created_by, created_at}); base is the state
--    of the target at capture, per item, so there is no base column on the
--    draft. version rises with every change (If-Match, API-07).
-- 2. status runs draft -> tested -> published, or to discarded; published
--    and discarded rows are immutable (application). published_at is set
--    exactly for published drafts (metamodel_draft_published_at_check).
-- 3. created_by is a surrogate without foreign key, like job.created_by.
-- 4. Row level security with ENABLE and FORCE and one policy FOR ALL
--    (pattern 000115); truncate guard of WP-352.

CREATE TABLE metamodel_draft (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'draft'
        CONSTRAINT metamodel_draft_status_check CHECK (status IN ('draft', 'tested', 'published', 'discarded')),
    created_by      UUID,
    items           JSONB NOT NULL DEFAULT '[]'::jsonb,
    test_report     JSONB,
    version         BIGINT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ,
    CONSTRAINT metamodel_draft_published_at_check CHECK ((status = 'published') = (published_at IS NOT NULL))
);
CREATE INDEX idx_metamodel_draft_org_status ON metamodel_draft (organization_id, status, created_at DESC);
CREATE TRIGGER trg_metamodel_draft_updated_at BEFORE UPDATE ON metamodel_draft
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE metamodel_draft ENABLE ROW LEVEL SECURITY;
ALTER TABLE metamodel_draft FORCE ROW LEVEL SECURITY;
CREATE POLICY metamodel_draft_isolation ON metamodel_draft FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON metamodel_draft
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
