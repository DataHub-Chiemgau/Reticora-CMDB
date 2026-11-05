-- Migration 000270: snapshots of the asset state (WP-795, AUD-08).
--
-- 1. asset_snapshot holds the replayed state of an asset at taken_at: the
--    weekly system job history.weekly_snapshot (reason weekly, with the
--    module inventory only) and the first snapshot of an asset without a
--    recorded creation (reason initial, from its live row). The replay of
--    an asset at T (GET /api/v1/assets/{id}/as-of) starts at the last
--    snapshot with taken_at <= T and replays the entity_change rows of the
--    asset after it. state is the document {fields, deleted} with the
--    personal values as vault surrogates (SEC-05), schema_version its
--    format version. asset_id has no foreign key, like
--    entity_change.entity_id: assets are deleted for good, and a snapshot
--    outlives the deletion until the retention (so K19 does not apply). The
--    code only inserts; an UPDATE comes only from the scope propagation.
-- 2. client_id and site_id are those of the asset (asset_snapshot_scope,
--    BEFORE INSERT, read with the scope widened); a snapshot of an asset
--    that is gone keeps the written scope. propagate_entity_change_scope
--    (WP-794, 000250) re-scopes the snapshots of an asset that changes
--    client or site as well.
-- 3. Policies as on entity_change (000250): USING with the client and site
--    predicate (NULL stays readable), WITH CHECK without NULL for scoped
--    principals, restrictive UPDATE and DELETE policies; the TRUNCATE guard
--    of the tenant tables (WP-352).

-- ─── 1. Table ──────────────────────────────────────────────────────────────────

CREATE TABLE asset_snapshot (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID,
    site_id         UUID,
    asset_id        UUID NOT NULL,
    taken_at        TIMESTAMPTZ NOT NULL,
    reason          TEXT NOT NULL CONSTRAINT asset_snapshot_reason_check CHECK (reason IN ('weekly', 'initial')),
    state           JSONB NOT NULL,
    schema_version  INTEGER NOT NULL
);

CREATE INDEX idx_asset_snapshot_asset_taken ON asset_snapshot (organization_id, asset_id, taken_at DESC);

-- ─── 2. Scope ──────────────────────────────────────────────────────────────────

CREATE FUNCTION asset_snapshot_derive_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    asset_client UUID;
    asset_site UUID;
    known boolean;
BEGIN
    SELECT a.client_id, a.site_id INTO asset_client, asset_site
      FROM asset a WHERE a.id = NEW.asset_id AND a.organization_id = NEW.organization_id;
    known := FOUND;
    PERFORM app_scope_restore(previous);
    IF known THEN
        NEW.client_id := asset_client;
        NEW.site_id := asset_site;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER asset_snapshot_scope BEFORE INSERT ON asset_snapshot
    FOR EACH ROW EXECUTE FUNCTION asset_snapshot_derive_scope();

-- propagate_entity_change_scope re-scopes the entity_change rows of the
-- asset or CI (TG_ARGV[0]) whose client or site changed, and the
-- asset_snapshot rows of an asset.
CREATE OR REPLACE FUNCTION propagate_entity_change_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE entity_change SET client_id = NEW.client_id, site_id = NEW.site_id
     WHERE organization_id = NEW.organization_id AND entity_type = TG_ARGV[0] AND entity_id = NEW.id
       AND (client_id IS DISTINCT FROM NEW.client_id OR site_id IS DISTINCT FROM NEW.site_id);
    IF TG_ARGV[0] = 'asset' THEN
        UPDATE asset_snapshot SET client_id = NEW.client_id, site_id = NEW.site_id
         WHERE organization_id = NEW.organization_id AND asset_id = NEW.id
           AND (client_id IS DISTINCT FROM NEW.client_id OR site_id IS DISTINCT FROM NEW.site_id);
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- ─── 3. Policies ───────────────────────────────────────────────────────────────

ALTER TABLE asset_snapshot ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_snapshot FORCE ROW LEVEL SECURITY;

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
        'CREATE POLICY asset_snapshot_isolation ON asset_snapshot '
        'USING (%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s)) '
            'AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))) '
        'WITH CHECK (%6$s)',
        org_match, client_all, client_ids, site_all, site_ids, writable);
    EXECUTE format('CREATE POLICY asset_snapshot_scoped_update ON asset_snapshot AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                   writable, writable);
    EXECUTE format('CREATE POLICY asset_snapshot_scoped_delete ON asset_snapshot AS RESTRICTIVE FOR DELETE USING (%s)', writable);
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON asset_snapshot
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
