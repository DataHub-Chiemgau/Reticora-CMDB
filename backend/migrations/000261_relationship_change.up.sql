-- WP-296 (REL-09, EVT-03, AUD-07): the history of the stored relationships.
--
-- relationship_change holds one row per mutation of an edge of
-- ci_relationship (relationship/history.go, RecordChangeTx): the kind of the
-- change, the changed fields, the edge before and after it (personal values
-- sealed as vault surrogates, SEC-05), who made it (actor_id, actor_type),
-- its source and the request and correlation id. The row outlives the edge
-- (relationship_id carries no foreign key); it goes with an endpoint CI on
-- its hard delete, like ci_change.
--
-- 1. Table with the tenant foreign keys of both endpoints (K19, pattern
--    ci_relationship_source_tenant_fkey of 000056). change_kind already
--    holds the kinds of WP-771 (refreshed, rewired, restored) and reason its
--    reasons, so that work package needs no migration.
-- 2. Scope: the trigger relationship_change_scope derives the client and
--    site of both endpoints (derive_edge_scope in the version of 000142: an
--    org-wide endpoint leaves its columns NULL). When a CI changes its client
--    or site, relationship_change_scope_propagation re-derives the rows of
--    its edges with the scope widened (pattern ci_module_scope_propagation of
--    000064 and propagate_ci_scope of 000141, which stays unchanged).
-- 3. Row level security with ENABLE and FORCE: relationship_change_isolation
--    reads like ci_relationship_isolation (000063: client and site of both
--    endpoints in the scope or NULL) and writes with the write predicate of
--    000142 (E-62: per dimension one endpoint in the scope, the other in the
--    scope or NULL); the restrictive relationship_change_scoped_update and
--    relationship_change_scoped_delete use that predicate as well. The
--    TRUNCATE guard of WP-352 (database.TruncateGuardTrigger).

-- ─── 1. Table ────────────────────────────────────────────────────────────────

CREATE TABLE relationship_change (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    relationship_id UUID NOT NULL,
    source_ci_id UUID NOT NULL,
    target_ci_id UUID NOT NULL,
    rel_type TEXT NOT NULL,
    change_kind TEXT NOT NULL,
    changed_fields TEXT[] NOT NULL DEFAULT '{}',
    before JSONB,
    after JSONB,
    actor_id UUID,
    actor_type TEXT NOT NULL,
    source TEXT NOT NULL,
    reason TEXT,
    request_id TEXT,
    correlation_id TEXT,
    observed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    source_client_id UUID,
    source_site_id UUID,
    target_client_id UUID,
    target_site_id UUID,
    CONSTRAINT relationship_change_source_tenant_fkey FOREIGN KEY (source_ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT relationship_change_target_tenant_fkey FOREIGN KEY (target_ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT chk_relationship_change_kind CHECK (change_kind IN
        ('created', 'updated', 'verification_changed', 'refreshed', 'removed', 'suppressed', 'rewired', 'restored')),
    CONSTRAINT chk_relationship_change_source CHECK (source IN ('ui', 'api', 'collector', 'import', 'system')),
    CONSTRAINT chk_relationship_change_reason CHECK (reason IS NULL OR reason IN
        ('stale', 'suppressed', 'merge', 'unmerge', 'interface_removed'))
);

CREATE INDEX idx_relationship_change_relationship ON relationship_change (organization_id, relationship_id, observed_at DESC);
CREATE INDEX idx_relationship_change_source ON relationship_change (organization_id, source_ci_id, observed_at DESC);
CREATE INDEX idx_relationship_change_target ON relationship_change (organization_id, target_ci_id, observed_at DESC);

-- ─── 2. Scope ────────────────────────────────────────────────────────────────

CREATE TRIGGER relationship_change_scope BEFORE INSERT OR UPDATE ON relationship_change
    FOR EACH ROW EXECUTE FUNCTION derive_edge_scope();

CREATE FUNCTION propagate_relationship_change_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE relationship_change SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER relationship_change_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_relationship_change_scope();

-- ─── 3. Row level security ───────────────────────────────────────────────────

ALTER TABLE relationship_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_change FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    write_check text;
BEGIN
    write_check := format(
        '%1$s '
        'AND (%2$s OR ((source_client_id IS NULL OR source_client_id = ANY (%3$s)) '
            'AND (target_client_id IS NULL OR target_client_id = ANY (%3$s)) '
            'AND (source_client_id = ANY (%3$s) OR target_client_id = ANY (%3$s)))) '
        'AND (%4$s OR ((source_site_id IS NULL OR source_site_id = ANY (%5$s)) '
            'AND (target_site_id IS NULL OR target_site_id = ANY (%5$s)) '
            'AND (source_site_id = ANY (%5$s) OR target_site_id = ANY (%5$s))))',
        org_match, client_all, client_ids, site_all, site_ids);
    EXECUTE format(
        'CREATE POLICY relationship_change_isolation ON relationship_change '
        'USING (%1$s '
            'AND (%2$s OR ((source_client_id IS NULL OR source_client_id = ANY (%3$s)) '
                'AND (target_client_id IS NULL OR target_client_id = ANY (%3$s)))) '
            'AND (%4$s OR ((source_site_id IS NULL OR source_site_id = ANY (%5$s)) '
                'AND (target_site_id IS NULL OR target_site_id = ANY (%5$s))))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, write_check);
    EXECUTE format('CREATE POLICY relationship_change_scoped_update ON relationship_change AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   write_check, write_check);
    EXECUTE format('CREATE POLICY relationship_change_scoped_delete ON relationship_change AS RESTRICTIVE FOR DELETE USING (%s)',
                   write_check);
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON relationship_change
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
