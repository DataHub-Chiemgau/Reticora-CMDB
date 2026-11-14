-- Down of the Nachtrag to WP-910 (E-256): ipam_section_denied as of 000320
-- (only sections with entries are restricted), without acl_enabled.

CREATE OR REPLACE FUNCTION ipam_section_denied(p_level text) RETURNS uuid[]
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    org text := current_setting('app.org_id', true);
    uid text := current_setting('app.user_id', true);
    v_team_scope text := NULLIF(current_setting('app.team_scope', true), '');
    v_user uuid;
    key_teams uuid[] := '{}';
    previous text[];
    denied uuid[];
BEGIN
    IF p_level IS NULL OR p_level NOT IN ('read', 'write') THEN
        RAISE EXCEPTION 'invalid section permission level %', p_level USING ERRCODE = '22023';
    END IF;
    IF current_setting('app.system', true) = 'on' OR current_setting('app.scope_widened', true) = 'on'
       OR org IS NULL OR org = '' THEN
        RETURN '{}';
    END IF;
    IF uid ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
        v_user := uid::uuid;
    END IF;
    -- The team scope of an API key counts like memberships; only teams it
    -- names explicitly (the empty value "all teams" names none).
    IF current_setting('app.principal_type', true) = 'api_key' AND v_team_scope IS NOT NULL THEN
        key_teams := string_to_array(v_team_scope, ',')::uuid[];
    END IF;
    previous := app_scope_widen();
    BEGIN
        SELECT COALESCE(array_agg(DISTINCT p.section_id), '{}') INTO denied
          FROM ipam_section_permission p
         WHERE p.organization_id = org::uuid
           AND NOT EXISTS (
               SELECT 1 FROM ipam_section_permission q
                WHERE q.organization_id = p.organization_id AND q.section_id = p.section_id
                  AND (p_level = 'read' OR q.level = 'write')
                  AND (q.team_id = ANY (key_teams)
                       OR EXISTS (SELECT 1 FROM team_member m
                                   WHERE m.organization_id = q.organization_id AND m.team_id = q.team_id
                                     AND m.user_id = v_user)));
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
    RETURN denied;
END
$$;
ALTER FUNCTION ipam_section_denied(text) OWNER TO reticora_owner;

DROP TRIGGER trg_ipam_section_permission_acl ON ipam_section_permission;
DROP FUNCTION ipam_section_acl_enable();
DROP INDEX idx_ipam_section_acl;
ALTER TABLE ipam_section DROP COLUMN acl_enabled;
