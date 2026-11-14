-- WP-1127 (SEC-03, CMP-02): the compliance score is weighted by the
-- severity of the rule and kept as a daily snapshot for trends of at least
-- 400 days.
--
-- 1. compliance_settings: the severity weights of an organization
--    (default critical 10, high 5, medium 2, low 1), one row per
--    organization; RLS over app.org_id.
-- 2. compliance_score_snapshot: per organization, day, client, site,
--    dimension (overall, ci_type, category, severity) and group key the
--    sums passed, failed, passed_weight and failed_weight, written by the
--    system job compliance.score.snapshot (upsert per day, rows older than
--    400 days removed by the same job). client and site cascade (Konvention
--    19, pattern 000255): a snapshot row must not block deleting a client or
--    a site. RLS after Konvention 5 (E-09) with the client and site scope.
-- 3. An index on compliance_result for the aggregation by rule and status.

CREATE TABLE compliance_settings (
    organization_id uuid PRIMARY KEY REFERENCES organization (id) ON DELETE CASCADE,
    weights         jsonb NOT NULL DEFAULT '{"critical": 10, "high": 5, "medium": 2, "low": 1}'
        CONSTRAINT compliance_settings_weights_check CHECK (jsonb_typeof(weights) = 'object'),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_compliance_settings_updated_at BEFORE UPDATE ON compliance_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON compliance_settings
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE compliance_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY compliance_settings_isolation ON compliance_settings
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);

CREATE TABLE compliance_score_snapshot (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    day             date NOT NULL,
    client_id       uuid NULL,
    site_id         uuid NULL,
    dimension       text NOT NULL CONSTRAINT compliance_score_snapshot_dimension_check
                        CHECK (dimension IN ('overall', 'ci_type', 'category', 'severity')),
    group_key       text NOT NULL DEFAULT '',
    passed          integer NOT NULL DEFAULT 0 CONSTRAINT compliance_score_snapshot_passed_check CHECK (passed >= 0),
    failed          integer NOT NULL DEFAULT 0 CONSTRAINT compliance_score_snapshot_failed_check CHECK (failed >= 0),
    passed_weight   bigint NOT NULL DEFAULT 0 CONSTRAINT compliance_score_snapshot_passed_weight_check CHECK (passed_weight >= 0),
    failed_weight   bigint NOT NULL DEFAULT 0 CONSTRAINT compliance_score_snapshot_failed_weight_check CHECK (failed_weight >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT compliance_score_snapshot_key UNIQUE NULLS NOT DISTINCT
        (organization_id, day, client_id, site_id, dimension, group_key),
    CONSTRAINT compliance_score_snapshot_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT compliance_score_snapshot_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE CASCADE
);

CREATE INDEX idx_compliance_score_snapshot_dimension_day ON compliance_score_snapshot (organization_id, dimension, day);
CREATE INDEX idx_compliance_score_snapshot_client ON compliance_score_snapshot (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_compliance_score_snapshot_site ON compliance_score_snapshot (site_id) WHERE site_id IS NOT NULL;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON compliance_score_snapshot
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE compliance_score_snapshot ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_score_snapshot FORCE ROW LEVEL SECURITY;

-- Konvention 5 (E-09): a restricted principal reads the rows of its clients
-- and sites and those without; it writes only rows inside its scope.
DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::uuid';
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    scope_match CONSTANT text := 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_match CONSTANT text := 'site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[])';
    t CONSTANT text := 'compliance_score_snapshot';
BEGIN
    EXECUTE format('CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s) AND (%s OR site_id IS NULL OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_isolation', t, org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s AND (%s OR %s) AND (%s OR %s)) '
        || 'WITH CHECK (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_scoped_update', t, org_match, scope_all, scope_match, site_all, site_match,
        org_match, scope_all, scope_match, site_all, site_match);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s AND (%s OR %s) AND (%s OR %s))',
        t || '_scoped_delete', t, org_match, scope_all, scope_match, site_all, site_match);
END
$$;

-- The aggregation of the score reads every result of the organization:
-- the index covers the columns of the aggregate, the group keys and the
-- row level security (client_id, site_id), so it runs as an index-only scan
-- of the organization's part instead of a scan of every tenant's results.
CREATE INDEX idx_compliance_result_rule_status ON compliance_result (organization_id, rule_id, status)
    INCLUDE (ci_id, ci_type_id, client_id, site_id);
