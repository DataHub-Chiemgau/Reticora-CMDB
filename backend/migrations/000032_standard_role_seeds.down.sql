-- Revert migration 000032.

-- 3. Trigger and seeding functions
DROP TRIGGER IF EXISTS seed_standard_roles_trigger ON organization;
DROP FUNCTION IF EXISTS trg_seed_standard_roles();
DROP FUNCTION IF EXISTS seed_standard_roles(UUID);

-- 2./1. Seeded roles and their grants (identified by is_builtin + standard names)
DELETE FROM role_permission rp
USING role r
WHERE rp.role_id = r.id
  AND r.is_builtin
  AND r.name IN ('org_admin', 'engineer', 'viewer', 'client_technician');

DELETE FROM role
WHERE is_builtin
  AND name IN ('org_admin', 'engineer', 'viewer', 'client_technician');

-- 0. Catalogue entries added by this migration
DELETE FROM permission
WHERE key IN ('user:manage', 'role:manage', 'webhook:manage', 'credential:manage', 'apikey:manage', 'citype:manage',
              'search:read', 'relationship:read', 'relationship:write', 'site:read', 'site:write',
              'entitlement:read', 'entitlement:manage', 'monitoring:read', 'monitoring:write',
              'export:run', 'discovery:ingest');
