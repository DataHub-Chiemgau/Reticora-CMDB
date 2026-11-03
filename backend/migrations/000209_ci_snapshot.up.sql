-- Migration 000209: snapshots of the CI state and the origin of merged
-- history rows (WP-312, AUD-08).
--
-- 1. ci_snapshot holds the replayed state of a CI at taken_at: the weekly
--    system job history.weekly_snapshot (reason weekly), the merge and the
--    unmerge (reasons merge and unmerge, from the live rows) and the first
--    snapshot of a CI without a recorded creation (reason initial). The
--    replay of a CI at T (history.PGRepository.TrailUpTo) starts at the
--    last snapshot with taken_at <= T and replays the ci_change rows after
--    it. state is the document {fields, deleted, merged_into_id, restore}
--    with the personal values as vault surrogates (SEC-05), schema_version
--    its format version, ci_version the version of the CI at taken_at,
--    relationships the edges of the CI when the snapshot was written.
--    ci_id has no foreign key, like entity_change.entity_id (000055): a
--    snapshot outlives the hard delete (CI-05) and the merge (CI-12) until
--    the retention. The code only inserts; an UPDATE comes only from the
--    scope propagation.
-- 2. client_id and site_id follow the CI (derive_ci_scope, 000061);
--    propagate_ci_scope (version of 000141, scope widened) re-derives the
--    rows of a CI that changes client or site.
-- 3. Policies as on ci_change (000063, 000079): USING with the client and
--    site predicate (NULL stays readable), WITH CHECK without NULL for
--    scoped principals, restrictive UPDATE and DELETE policies; the TRUNCATE
--    guard of the tenant tables (WP-352).
-- 4. ci_change.origin_ci_id names the CI a history row belonged to before a
--    merge moved it to the survivor; the replay of a CI reads the rows with
--    COALESCE(origin_ci_id, ci_id) = id. The unmerge writes the column back
--    from the merge snapshot.

-- ─── 1. Table ──────────────────────────────────────────────────────────────────

CREATE TABLE ci_snapshot (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID,
    site_id         UUID,
    ci_id           UUID NOT NULL,
    taken_at        TIMESTAMPTZ NOT NULL,
    reason          TEXT NOT NULL CONSTRAINT ci_snapshot_reason_check CHECK (reason IN ('weekly', 'merge', 'unmerge', 'initial')),
    state           JSONB NOT NULL,
    relationships   JSONB,
    schema_version  INTEGER NOT NULL,
    ci_version      BIGINT
);

CREATE INDEX idx_ci_snapshot_ci_taken ON ci_snapshot (organization_id, ci_id, taken_at DESC);

-- ─── 2. Scope ──────────────────────────────────────────────────────────────────

CREATE TRIGGER ci_snapshot_scope BEFORE INSERT OR UPDATE ON ci_snapshot
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');

-- A no-op UPDATE re-runs the derivation triggers of the dependent rows.
CREATE OR REPLACE FUNCTION propagate_ci_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE network_interface SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_contact SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_change SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_field_value SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_instance_field_definition SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE rack_mount SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE compliance_result SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE security_finding SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE discovery_result SET matched_ci_id = matched_ci_id WHERE matched_ci_id = NEW.id;
    UPDATE ci_relationship SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    UPDATE relationship_suppression SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    UPDATE ci_snapshot SET ci_id = ci_id WHERE ci_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- ─── 3. Policies ───────────────────────────────────────────────────────────────

ALTER TABLE ci_snapshot ENABLE ROW LEVEL SECURITY;
ALTER TABLE ci_snapshot FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    writable text := format('%1$s AND (%2$s OR client_id = ANY (%3$s)) AND (%4$s OR site_id = ANY (%5$s))',
                            org_match, client_all, client_ids, site_all, site_ids);
BEGIN
    EXECUTE format(
        'CREATE POLICY ci_snapshot_isolation ON ci_snapshot '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY ci_snapshot_scoped_update ON ci_snapshot AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY ci_snapshot_scoped_delete ON ci_snapshot AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ci_snapshot
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 4. Origin of merged history rows ─────────────────────────────────────────

ALTER TABLE ci_change ADD COLUMN origin_ci_id UUID;

CREATE INDEX idx_ci_change_origin ON ci_change (organization_id, origin_ci_id) WHERE origin_ci_id IS NOT NULL;
