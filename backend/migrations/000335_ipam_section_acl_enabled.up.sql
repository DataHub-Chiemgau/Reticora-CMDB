-- Nachtrag to WP-910 (IPM-10, TEN-05, E-256 option 1): a section stays
-- restricted after the last team with an entry is deleted.
--
-- * ipam_section.acl_enabled marks a section as restricted. The first entry
--   in ipam_section_permission sets it (trigger, scope widened so that the
--   writer need not see the section); deleting entries, also through the
--   cascade of a deleted team, leaves it set. Backfill: true for sections
--   with entries. The backfill neither touches updated_at nor writes
--   history rows (triggers disabled as in 000321).
-- * ipam_section_denied (version of 000320) denies every section with
--   acl_enabled or with entries for which the principal has no entry of a
--   team at the level, also when no entry is left (fail-closed). Only an explicit
--   PATCH /api/v1/ipam-sections/{id} with acl_enabled=false, which is
--   audited, opens such a section again.
-- * idx_ipam_section_acl: the restricted sections of an organization, read
--   once per statement by ipam_section_denied.

ALTER TABLE ipam_section ADD COLUMN acl_enabled boolean NOT NULL DEFAULT false;

ALTER TABLE ipam_section DISABLE TRIGGER USER;
UPDATE ipam_section s SET acl_enabled = true
 WHERE EXISTS (SELECT 1 FROM ipam_section_permission p WHERE p.organization_id = s.organization_id AND p.section_id = s.id);
ALTER TABLE ipam_section ENABLE TRIGGER USER;

CREATE INDEX idx_ipam_section_acl ON ipam_section (organization_id) WHERE acl_enabled;

CREATE FUNCTION ipam_section_acl_enable() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ipam_section SET acl_enabled = true
     WHERE organization_id = NEW.organization_id AND id = NEW.section_id AND NOT acl_enabled;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_ipam_section_permission_acl AFTER INSERT ON ipam_section_permission
    FOR EACH ROW EXECUTE FUNCTION ipam_section_acl_enable();

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
        -- Every restricted section (acl_enabled, E-256) without an entry
        -- of a team of the principal at the level, also when no entry is
        -- left. A section with entries counts as restricted even without
        -- the flag (fail-closed, should a release ever race an entry).
        SELECT COALESCE(array_agg(s.id), '{}') INTO denied
          FROM ipam_section s
         WHERE s.organization_id = org::uuid
           AND (s.acl_enabled OR EXISTS (SELECT 1 FROM ipam_section_permission e
                                          WHERE e.organization_id = s.organization_id AND e.section_id = s.id))
           AND NOT EXISTS (
               SELECT 1 FROM ipam_section_permission q
                WHERE q.organization_id = s.organization_id AND q.section_id = s.id
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
