-- Migration 000054: default organization seed
--
-- The bundled Keycloak realm (deploy/keycloak/realm-reticora.json) places every
-- imported user in the organization group 00000000-0000-0000-0000-000000000001;
-- the backend derives the tenant from the first UUID-named group in the ID
-- token and the identity provisioner (identity.UserProvisioner →
-- user.PGRepository.EnsureUser) inserts the first-login app_user row with that
-- organization id. Without a matching organization row that insert fails with
--   ERROR: insert or update on table "app_user" violates foreign key
--   constraint "app_user_organization_id_fkey" (SQLSTATE 23503)
-- so a fresh installation could not log in at all.
--
-- The INSERT runs as the migration role (table owner), so the
-- org_isolation RLS policy does not apply to it. It is idempotent: re-running
-- keeps the existing row, and a conflicting slug from a manually created
-- organization is left untouched.
--
-- Standard roles for this organization are seeded by the
-- seed_standard_roles_trigger on organization (migration 000032).

INSERT INTO organization (id, name, slug)
VALUES ('00000000-0000-0000-0000-000000000001', 'Reticora Demo', 'reticora-demo')
ON CONFLICT (id) DO NOTHING;
