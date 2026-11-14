-- Down of 000364: the function of 000302 (WP-908), the unconditional
-- updated_at trigger of 000036 and neither scan_applied_at nor
-- scan_sync_failed_at.

DROP TRIGGER trg_subnet_updated_at ON subnet;
CREATE TRIGGER trg_subnet_updated_at BEFORE UPDATE ON subnet
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE subnet DROP COLUMN scan_sync_failed_at, DROP COLUMN scan_applied_at;

CREATE OR REPLACE FUNCTION ipam_change_record() RETURNS trigger
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
