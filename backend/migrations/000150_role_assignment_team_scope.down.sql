-- Reverts WP-391: role assignments without team scope (000005, 000014).
--
-- Team-scoped assignments are removed first: without scope_team_id a
-- role_assignment row would grant its role org-wide, and the old check of
-- user_custom_role refuses scope_type team. Assignments merged or removed by
-- the up migration are not restored.

DELETE FROM role_assignment WHERE scope_team_id IS NOT NULL;
DELETE FROM user_custom_role WHERE scope_type = 'team';

ALTER TABLE user_custom_role DROP CONSTRAINT scope_type_check,
    ADD CONSTRAINT scope_type_check CHECK (scope_type IN ('organization', 'client', 'site'));

ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_scope_key,
    DROP CONSTRAINT role_assignment_single_scope,
    DROP COLUMN scope_team_id,
    ADD CONSTRAINT role_assignment_user_id_role_id_scope_client_id_scope_site__key UNIQUE (user_id, role_id, scope_client_id, scope_site_id);

ALTER TABLE team DROP CONSTRAINT team_id_organization_key;
