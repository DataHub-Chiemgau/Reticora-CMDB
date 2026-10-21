-- WP-391 (RBA-03, TEN-04; F-049, E-11): role assignments with a team scope.
--
-- RBA-03 defines role_assignment(role, scope_kind org|client|site|team,
-- scope_id); role_assignment had no team column and user_custom_role allowed
-- no scope_type team, so app.team_scope was 'all' for every real user and the
-- team predicates of 000065 restricted nobody.
--
-- 1. role_assignment.scope_team_id references a team of the same
--    organization. Deleting the team removes the assignments scoped to it,
--    like its memberships (team_member): a team scope never turns into an
--    org-wide one.
-- 2. At most one scope column is set. The grant resolution derives the client
--    of a site assignment from the site (WP-390), so a client next to the
--    site is redundant: rows naming the client of their site keep the site
--    only; rows naming another client granted nothing (WP-390) and are
--    removed. Both counts are reported.
-- 3. Uniqueness over all scope columns with NULLS NOT DISTINCT: the old
--    UNIQUE (user_id, role_id, scope_client_id, scope_site_id) never fired
--    for NULL scopes, so an org-wide assignment could be stored repeatedly.
--    Duplicates are removed first (the oldest row stays) and reported.
-- 4. user_custom_role.scope_type allows team.
--
-- RLS of both tables is unchanged.

ALTER TABLE team ADD CONSTRAINT team_id_organization_key UNIQUE (id, organization_id);

ALTER TABLE role_assignment ADD COLUMN scope_team_id UUID,
    ADD CONSTRAINT role_assignment_scope_team_id_fkey FOREIGN KEY (scope_team_id, organization_id)
        REFERENCES team (id, organization_id) ON DELETE CASCADE;

DO $$
DECLARE
    redundant INTEGER;
    contradictory INTEGER;
    duplicates INTEGER;
BEGIN
    UPDATE role_assignment ra SET scope_client_id = NULL
      FROM site s
     WHERE ra.scope_client_id IS NOT NULL AND ra.scope_site_id IS NOT NULL
       AND s.id = ra.scope_site_id AND s.organization_id = ra.organization_id
       AND s.client_id = ra.scope_client_id;
    GET DIAGNOSTICS redundant = ROW_COUNT;
    IF redundant > 0 THEN
        RAISE NOTICE 'role_assignment: % site assignments keep the site only (client of the site)', redundant;
    END IF;

    DELETE FROM role_assignment WHERE scope_client_id IS NOT NULL AND scope_site_id IS NOT NULL;
    GET DIAGNOSTICS contradictory = ROW_COUNT;
    IF contradictory > 0 THEN
        RAISE NOTICE 'role_assignment: removed % assignments of a site outside their client', contradictory;
    END IF;

    DELETE FROM role_assignment ra
     WHERE EXISTS (
        SELECT 1 FROM role_assignment older
         WHERE older.user_id = ra.user_id AND older.role_id = ra.role_id
           AND older.scope_client_id IS NOT DISTINCT FROM ra.scope_client_id
           AND older.scope_site_id IS NOT DISTINCT FROM ra.scope_site_id
           AND (older.created_at, older.id) < (ra.created_at, ra.id));
    GET DIAGNOSTICS duplicates = ROW_COUNT;
    IF duplicates > 0 THEN
        RAISE NOTICE 'role_assignment: removed % duplicate assignments (the oldest stays)', duplicates;
    END IF;
END
$$;

ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_user_id_role_id_scope_client_id_scope_site__key,
    ADD CONSTRAINT role_assignment_single_scope CHECK (num_nonnulls(scope_client_id, scope_site_id, scope_team_id) <= 1),
    ADD CONSTRAINT role_assignment_scope_key UNIQUE NULLS NOT DISTINCT (user_id, role_id, scope_client_id, scope_site_id, scope_team_id);

ALTER TABLE user_custom_role DROP CONSTRAINT scope_type_check,
    ADD CONSTRAINT scope_type_check CHECK (scope_type IN ('organization', 'client', 'site', 'team'));
