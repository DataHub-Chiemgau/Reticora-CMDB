-- WP-908 (IPM-08, E-213): the IPAM change history.
--
-- ipam_change is append-only: one row per created or deleted object (the
-- object as new_value or old_value) and one row per changed field of an
-- updated object of subnet, ip_address, vlan, vrf and ipam_section. The
-- rows outlive the object (object_id carries no foreign key); actor_id is
-- the surrogate of app.user_id without a foreign key (Konvention 16).
--
-- 1. Table with the derived scope columns client_id and site_id of the
--    object after the change (before it for delete), without foreign keys
--    (derived scope columns, pattern relationship_change of 000261).
-- 2. ipam_change_record(): statement triggers with transition tables
--    write the history set-based in the writing transaction, one INSERT
--    per statement; every write path, including those of later work
--    packages (allocation, import, scan) and cascades such as
--    subnet_propagate_vrf (000092), is recorded without code of its own.
--    source is the transaction setting app.ipam_change_source (default
--    manual). updated_at, created_at and address_host are not recorded as
--    field changes, nor the observation bookkeeping last_seen_at and
--    miss_count of a discovery sync (E-213, Nachtrag 2026-11-12). The
--    rows are written with the scope widened (app_scope_widen,
--    000139/000217), so a cascade across scope borders passes WITH CHECK. Changes of a row whose organization is
--    being deleted are not recorded (the cascade of its deletion).
-- 3. Row level security with ENABLE and FORCE (Konvention 5, E-09): rows
--    of the caller's clients and sites and org-wide rows are readable; a
--    restrictive DELETE policy admits only principals without client and
--    site restriction (retention); reticora_app has no UPDATE; the
--    TRUNCATE guard.

-- ─── 1. Table ────────────────────────────────────────────────────────────────

CREATE TABLE ipam_change (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    object_kind text NOT NULL,
    object_id uuid NOT NULL,
    actor_id uuid,
    source text NOT NULL DEFAULT 'manual',
    change_type text NOT NULL,
    field_name text,
    old_value jsonb,
    new_value jsonb,
    changed_at timestamptz NOT NULL DEFAULT now(),
    client_id uuid,
    site_id uuid,
    CONSTRAINT chk_ipam_change_object_kind CHECK (object_kind IN ('subnet', 'ip_address', 'vlan', 'vrf', 'section')),
    CONSTRAINT chk_ipam_change_source CHECK (source IN ('manual', 'discovery', 'scan', 'import', 'allocation')),
    CONSTRAINT chk_ipam_change_type CHECK (change_type IN ('create', 'update', 'delete'))
);

CREATE INDEX idx_ipam_change_object ON ipam_change (organization_id, object_kind, object_id, changed_at DESC);

-- ─── 2. Triggers ─────────────────────────────────────────────────────────────

CREATE FUNCTION ipam_change_record() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    kind CONSTANT text := TG_ARGV[0];
    src CONSTANT text := COALESCE(NULLIF(current_setting('app.ipam_change_source', true), ''), 'manual');
    uid CONSTANT text := current_setting('app.user_id', true);
    actor uuid;
    previous text[];
BEGIN
    IF uid ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
        actor := uid::uuid;
    END IF;
    previous := app_scope_widen();
    IF TG_OP = 'INSERT' THEN
        INSERT INTO ipam_change (organization_id, object_kind, object_id, actor_id, source, change_type, new_value, client_id, site_id)
        SELECT n.organization_id, kind, n.id, actor, src, 'create', j.v - 'address_host',
               (j.v->>'client_id')::uuid, (j.v->>'site_id')::uuid
          FROM ipam_change_new n
         CROSS JOIN LATERAL (SELECT to_jsonb(n) AS v) j
         WHERE EXISTS (SELECT 1 FROM organization o WHERE o.id = n.organization_id);
    ELSIF TG_OP = 'UPDATE' THEN
        INSERT INTO ipam_change (organization_id, object_kind, object_id, actor_id, source, change_type, field_name,
                                 old_value, new_value, client_id, site_id)
        SELECT n.organization_id, kind, n.id, actor, src, 'update', f.key, j.ov -> f.key, f.value,
               (j.nv->>'client_id')::uuid, (j.nv->>'site_id')::uuid
          FROM ipam_change_new n
          JOIN ipam_change_old o ON o.id = n.id
         CROSS JOIN LATERAL (SELECT to_jsonb(n) AS nv, to_jsonb(o) AS ov) j
         CROSS JOIN LATERAL jsonb_each(j.nv) f
         WHERE f.key NOT IN ('updated_at', 'created_at', 'address_host', 'last_seen_at', 'miss_count')
           AND (j.ov -> f.key) IS DISTINCT FROM f.value
           AND EXISTS (SELECT 1 FROM organization org WHERE org.id = n.organization_id);
    ELSE
        INSERT INTO ipam_change (organization_id, object_kind, object_id, actor_id, source, change_type, old_value, client_id, site_id)
        SELECT o.organization_id, kind, o.id, actor, src, 'delete', j.v - 'address_host',
               (j.v->>'client_id')::uuid, (j.v->>'site_id')::uuid
          FROM ipam_change_old o
         CROSS JOIN LATERAL (SELECT to_jsonb(o) AS v) j
         WHERE EXISTS (SELECT 1 FROM organization org WHERE org.id = o.organization_id);
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

DO $$
DECLARE
    t record;
BEGIN
    FOR t IN SELECT * FROM (VALUES ('subnet', 'subnet'), ('ip_address', 'ip_address'), ('vlan', 'vlan'),
                                   ('vrf', 'vrf'), ('ipam_section', 'section')) AS v(tbl, kind) LOOP
        EXECUTE format('CREATE TRIGGER trg_%1$s_ipam_change_insert AFTER INSERT ON %1$I '
                       'REFERENCING NEW TABLE AS ipam_change_new FOR EACH STATEMENT EXECUTE FUNCTION ipam_change_record(%2$L)',
                       t.tbl, t.kind);
        EXECUTE format('CREATE TRIGGER trg_%1$s_ipam_change_update AFTER UPDATE ON %1$I '
                       'REFERENCING OLD TABLE AS ipam_change_old NEW TABLE AS ipam_change_new FOR EACH STATEMENT '
                       'EXECUTE FUNCTION ipam_change_record(%2$L)', t.tbl, t.kind);
        EXECUTE format('CREATE TRIGGER trg_%1$s_ipam_change_delete AFTER DELETE ON %1$I '
                       'REFERENCING OLD TABLE AS ipam_change_old FOR EACH STATEMENT EXECUTE FUNCTION ipam_change_record(%2$L)',
                       t.tbl, t.kind);
    END LOOP;
END
$$;

-- ─── 3. Row level security ───────────────────────────────────────────────────

ALTER TABLE ipam_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE ipam_change FORCE ROW LEVEL SECURITY;

CREATE POLICY ipam_change_isolation ON ipam_change
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

-- The retention deletes; a principal restricted to clients or sites never.
CREATE POLICY ipam_change_scoped_delete ON ipam_change AS RESTRICTIVE FOR DELETE
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL
    );

REVOKE UPDATE ON ipam_change FROM reticora_app;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ipam_change
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
