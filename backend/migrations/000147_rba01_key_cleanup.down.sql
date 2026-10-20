-- Migration 000147 down: back to the catalogue and seed of migration 000081.
--
-- The retired keys return to the catalogue. Every holder of citype:manage
-- regains ci_type:manage, the key the CI-type routes required before 000147;
-- the standard roles regain their retired keys through the seed of 000081.
-- The other retired keys gated no route, so custom roles and API keys that
-- held them are not restored.

INSERT INTO permission (key, resource, action, description) VALUES
    ('ci_type:manage', 'ci_type', 'manage', 'Manage CI types'),
    ('credential:decrypt', 'credential', 'decrypt', 'Decrypt stored credential secrets'),
    ('asset:assign', 'asset', 'assign', 'Assign assets to users or teams'),
    ('credential:write', 'credential', 'write', 'Manage credentials'),
    ('permission:write', 'permission', 'write', 'Replace role permission grants'),
    ('role:write', 'role', 'write', 'Manage roles'),
    ('user:write', 'user', 'write', 'Manage users and teams'),
    ('webhook:write', 'webhook', 'write', 'Manage webhooks')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT organization_id, role_id, 'ci_type:manage' FROM role_permission WHERE permission_key = 'citype:manage'
ON CONFLICT DO NOTHING;

UPDATE role SET permissions = (
    SELECT jsonb_agg(DISTINCT k ORDER BY k) FROM (
        SELECT jsonb_array_elements_text(role.permissions) AS k UNION SELECT 'ci_type:manage') t)
 WHERE permissions ? 'citype:manage';

UPDATE custom_role SET permissions = (
    SELECT jsonb_agg(DISTINCT k ORDER BY k) FROM (
        SELECT jsonb_array_elements_text(custom_role.permissions) AS k UNION SELECT 'ci_type:manage') t)
 WHERE jsonb_typeof(permissions) = 'array' AND permissions ? 'citype:manage';

UPDATE api_key SET permissions = ARRAY(
    SELECT DISTINCT k FROM unnest(api_key.permissions || ARRAY['ci_type:manage']) AS k ORDER BY 1)
 WHERE 'citype:manage' = ANY (permissions);

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
                    'agent:manage', 'agent:read', 'ai:read', 'asset:assign',
                    'asset:move', 'asset:read', 'asset:reserve', 'asset:write',
                    'assignment:read', 'assignment:write', 'ci:delete', 'ci:read',
                    'ci:write', 'ci_instance_attribute:manage', 'collector:manage', 'compliance:read',
                    'compliance:write', 'consumable:read', 'consumable:write', 'contact:read',
                    'contact:write', 'credential:decrypt', 'credential:manage', 'credential:read',
                    'credential:write', 'desk:read', 'desk:write', 'discovery:ingest',
                    'discovery:manage', 'discovery:read', 'disposal:read', 'disposal:write',
                    'document:read', 'document:write', 'entitlement:read', 'export:run',
                    'form:read', 'form:write', 'iga:read', 'ipam:read',
                    'ipam:write', 'job:read', 'key:read', 'key:write',
                    'lifecycle:transition', 'location:read', 'location:write', 'maintenance:read',
                    'maintenance:write', 'monitoring:read', 'monitoring:write', 'order:read',
                    'order:write', 'override:write', 'permission:read', 'rack:read',
                    'rack:write', 'reconciliation:manage', 'relationship:read', 'relationship:write',
                    'review:resolve', 'role:read', 'saved_view:read', 'saved_view:write',
                    'search:read', 'search:write', 'security:read', 'security:write',
                    'sla:read', 'stocktake:read', 'stocktake:write', 'ticket:read',
                    'ticket:write', 'topology:read', 'training:read', 'training:write',
                    'user:read', 'vrf:manage', 'webhook:read', 'workflow:read',
                    'workflow:write'
                ]),
            ('viewer', 'Read-only access across the organization; no credential, audit or administration rights', 'org',
                ARRAY[
                    'agent:read', 'asset:read', 'assignment:read', 'ci:read',
                    'compliance:read', 'consumable:read', 'contact:read', 'desk:read',
                    'discovery:read', 'disposal:read', 'document:read', 'entitlement:read',
                    'form:read', 'iga:read', 'ipam:read', 'key:read',
                    'location:read', 'maintenance:read', 'monitoring:read', 'order:read',
                    'permission:read', 'rack:read', 'relationship:read', 'role:read',
                    'saved_view:read', 'search:read', 'security:read', 'sla:read',
                    'stocktake:read', 'ticket:read', 'topology:read', 'training:read',
                    'user:read', 'webhook:read', 'workflow:read'
                ]),
            ('client_technician', 'Client-scoped technician: reads operational data and works tickets', 'client',
                ARRAY[
                    'asset:read', 'assignment:read', 'ci:read', 'ci:write',
                    'consumable:read', 'contact:read', 'contact:write', 'desk:read',
                    'document:read', 'export:run', 'ipam:read', 'job:read',
                    'key:read', 'location:read', 'maintenance:read', 'monitoring:read',
                    'order:read', 'rack:read', 'rack:write', 'relationship:read',
                    'relationship:write', 'review:resolve', 'saved_view:read', 'search:read',
                    'stocktake:read', 'stocktake:write', 'ticket:read', 'ticket:write',
                    'topology:read', 'training:read'
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

SELECT seed_standard_roles(id) FROM organization;
