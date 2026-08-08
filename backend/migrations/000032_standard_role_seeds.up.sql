-- Migration 000032: standard role seeds
--
-- The spec defines four standard roles (org_admin, engineer, viewer,
-- client_technician) that must exist in every organization. Grants are
-- materialized in role_permission (migration 000026) and mirrored into
-- role.permissions (migration 000005 JSONB column); EffectivePermissions reads
-- both sources and intersects with the permission catalogue.
--
-- Notes:
-- * The runtime also uses the `:manage` keys (user:manage, role:manage,
--   webhook:manage, credential:manage) and apikey:manage / citype:manage, which
--   were missing from the catalogue migration 000026 — they are inserted here so
--   org_admin receives them (SELECT key FROM permission) and effective-permission
--   resolution stops dropping them.
-- * A trigger on organization seeds the roles for organizations created after
--   this migration.

-- ─── 0. Missing catalogue entries ───────────────────────────────────────────────

INSERT INTO permission (key, resource, action, description) VALUES
    ('user:manage', 'user', 'manage', 'Administer users, teams and clients'),
    ('role:manage', 'role', 'manage', 'Administer roles'),
    ('webhook:manage', 'webhook', 'manage', 'Administer webhook subscriptions'),
    ('credential:manage', 'credential', 'manage', 'Administer credentials'),
    ('apikey:manage', 'apikey', 'manage', 'Manage API keys'),
    ('citype:manage', 'citype', 'manage', 'Manage CI types'),
    -- Catalogue gaps: defined in the Go catalogue (internal/permission/catalog.go)
    -- and enforced by authz, but never inserted by earlier migrations.
    ('search:read', 'search', 'read', 'Search configuration items'),
    ('relationship:read', 'relationship', 'read', 'Read CI relationships'),
    ('relationship:write', 'relationship', 'write', 'Manage CI relationships'),
    ('site:read', 'site', 'read', 'Read sites, buildings and rooms'),
    ('site:write', 'site', 'write', 'Manage sites, buildings and rooms'),
    ('entitlement:read', 'entitlement', 'read', 'Read tenant entitlements'),
    ('entitlement:manage', 'entitlement', 'manage', 'Manage tenant entitlements'),
    ('monitoring:read', 'monitoring', 'read', 'Read metrics and alerts'),
    ('monitoring:write', 'monitoring', 'write', 'Ingest metrics and manage alerts'),
    ('export:run', 'export', 'run', 'Run data exports'),
    ('discovery:ingest', 'discovery', 'ingest', 'Ingest collector discovery data')
ON CONFLICT (key) DO UPDATE SET
    resource = EXCLUDED.resource,
    action = EXCLUDED.action,
    description = EXCLUDED.description,
    updated_at = now();

-- ─── 1. Seeding function (permanent; reused by the trigger) ─────────────────────

CREATE OR REPLACE FUNCTION seed_standard_roles(p_organization_id UUID)
RETURNS VOID AS $$
DECLARE
    std RECORD;
    rid UUID;
BEGIN
    FOR std IN
        SELECT * FROM (VALUES
            ('org_admin', 'Organization administrator with full access', 'org',
                (SELECT array_agg(key ORDER BY key) FROM permission)),
            ('engineer', 'Technical staff: manages CIs, topology, discovery, assets, IPAM, racks and operations', 'org',
                ARRAY[
                    'ci:read', 'ci:write', 'ci:delete',
                    'topology:read',
                    'discovery:read', 'discovery:write', 'discovery:ingest',
                    'asset:read', 'asset:write',
                    'assignment:read', 'assignment:write',
                    'document:read', 'document:write',
                    'stocktake:read', 'stocktake:write',
                    'ticket:read', 'ticket:write',
                    'user:read',
                    'role:read',
                    'permission:read',
                    'sla:read',
                    'webhook:read',
                    'credential:read', 'credential:write',
                    'audit:read',
                    'ipam:read', 'ipam:write',
                    'rack:read', 'rack:write',
                    'contact:read', 'contact:write',
                    'search:read', 'search:write',
                    'ai:read',
                    'relationship:read', 'relationship:write',
                    'site:read', 'site:write',
                    'entitlement:read',
                    'form:read', 'form:write',
                    'workflow:read', 'workflow:write',
                    'compliance:read', 'compliance:write',
                    'monitoring:read', 'monitoring:write',
                    'iga:read',
                    'export:run'
                ]),
            ('viewer', 'Read-only access across the organization', 'org',
                ARRAY[
                    'ci:read',
                    'topology:read',
                    'discovery:read',
                    'asset:read',
                    'assignment:read',
                    'document:read',
                    'stocktake:read',
                    'ticket:read',
                    'user:read',
                    'role:read',
                    'permission:read',
                    'sla:read',
                    'webhook:read',
                    'credential:read',
                    'audit:read',
                    'ipam:read',
                    'rack:read',
                    'contact:read',
                    'search:read',
                    'relationship:read',
                    'site:read',
                    'entitlement:read',
                    'form:read',
                    'workflow:read',
                    'compliance:read',
                    'monitoring:read',
                    'iga:read'
                ]),
            ('client_technician', 'Client-scoped technician: reads operational data and works tickets', 'client',
                ARRAY[
                    'ci:read',
                    'topology:read',
                    'asset:read',
                    'assignment:read',
                    'document:read',
                    'stocktake:read', 'stocktake:write',
                    'ticket:read', 'ticket:write',
                    'ipam:read',
                    'rack:read',
                    'contact:read',
                    'search:read',
                    'relationship:read',
                    'site:read',
                    'monitoring:read'
                ])
        ) AS v(name, description, scope, perms)
    LOOP
        INSERT INTO role (organization_id, name, description, permissions, is_builtin, scope)
        VALUES (p_organization_id, std.name, std.description, to_jsonb(std.perms), true, std.scope)
        ON CONFLICT (organization_id, name) DO UPDATE
            SET permissions = EXCLUDED.permissions,
                is_builtin  = true,
                scope       = EXCLUDED.scope,
                description = COALESCE(role.description, EXCLUDED.description)
        RETURNING id INTO rid;

        INSERT INTO role_permission (organization_id, role_id, permission_key)
        SELECT p_organization_id, rid, unnest(std.perms)
        ON CONFLICT DO NOTHING;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

-- ─── 2. Seed all existing organizations ─────────────────────────────────────────

DO $$
DECLARE
    org RECORD;
BEGIN
    FOR org IN SELECT id FROM organization LOOP
        PERFORM seed_standard_roles(org.id);
    END LOOP;
END
$$;

-- ─── 3. Trigger for future organizations ────────────────────────────────────────

CREATE OR REPLACE FUNCTION trg_seed_standard_roles()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM seed_standard_roles(NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS seed_standard_roles_trigger ON organization;
CREATE TRIGGER seed_standard_roles_trigger
    AFTER INSERT ON organization
    FOR EACH ROW EXECUTE FUNCTION trg_seed_standard_roles();
