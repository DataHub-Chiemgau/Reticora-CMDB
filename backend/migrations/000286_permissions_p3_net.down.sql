-- Migration 000286 down: back to the catalogue and seed of migration 000285.
-- The four keys leave every grant (role_permission cascades on delete,
-- role.permissions, custom_role.permissions and api_key.permissions are
-- cleaned explicitly) and the catalogue; the seed of 000285 returns.

UPDATE role SET permissions = (
    SELECT COALESCE(jsonb_agg(k ORDER BY k), '[]'::jsonb) FROM jsonb_array_elements_text(role.permissions) AS e(k)
     WHERE k NOT IN ('credential:bind', 'vault:manage', 'admp:holder_read', 'admp:manage'))
 WHERE permissions ?| ARRAY['credential:bind', 'vault:manage', 'admp:holder_read', 'admp:manage'];

UPDATE custom_role SET permissions = (
    SELECT COALESCE(jsonb_agg(k ORDER BY k), '[]'::jsonb) FROM jsonb_array_elements_text(custom_role.permissions) AS e(k)
     WHERE k NOT IN ('credential:bind', 'vault:manage', 'admp:holder_read', 'admp:manage'))
 WHERE jsonb_typeof(permissions) = 'array'
   AND permissions ?| ARRAY['credential:bind', 'vault:manage', 'admp:holder_read', 'admp:manage'];

UPDATE api_key SET permissions = ARRAY(
    SELECT k FROM unnest(api_key.permissions) AS k
     WHERE k NOT IN ('credential:bind', 'vault:manage', 'admp:holder_read', 'admp:manage') ORDER BY 1)
 WHERE permissions && ARRAY['credential:bind', 'vault:manage', 'admp:holder_read', 'admp:manage'];

DELETE FROM permission WHERE key IN ('credential:bind', 'vault:manage', 'admp:holder_read', 'admp:manage');

-- The seed of migration 000285, verbatim.
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
                    'admp:read', 'agent:manage', 'agent:read', 'ai:read',
                    'alert:acknowledge', 'asset:move', 'asset:read', 'asset:write',
                    'assignment:read', 'assignment:write', 'ci:delete', 'ci:read',
                    'ci:write', 'ci_instance_attribute:manage', 'collector:manage', 'compliance:read',
                    'compliance:write', 'contact:read', 'contact:write', 'contract:read',
                    'contract:write', 'credential:manage', 'credential:read', 'desk:read',
                    'desk:write', 'discovery:ingest', 'discovery:manage', 'discovery:read',
                    'disposal:read', 'disposal:write', 'document:read', 'document:write',
                    'entitlement:read', 'export:run', 'finding:read', 'finding:write',
                    'form:read', 'form:write', 'iga:read', 'import:run',
                    'ipam:read', 'ipam:write', 'job:read', 'key:read',
                    'key:write', 'lifecycle:transition', 'location:read', 'location:write',
                    'maintenance:read', 'maintenance:write', 'monitoring:read', 'monitoring:write',
                    'order:read', 'order:write', 'override:write', 'permission:read',
                    'rack:read', 'rack:write', 'reconciliation:manage', 'relationship:read',
                    'relationship:write', 'reservation:read', 'reservation:write', 'review:resolve',
                    'role:read', 'saved_view:read', 'saved_view:write', 'search:read',
                    'search:write', 'sla:read', 'stock:read', 'stock:write',
                    'stocktake:complete', 'stocktake:read', 'stocktake:write', 'tag:manage',
                    'ticket:read', 'ticket:write', 'topology:read', 'training:read',
                    'training:write', 'user:read', 'vrf:manage', 'webhook:read',
                    'workflow:read', 'workflow:write'
                ]),
            ('viewer', 'Read-only access across the organization; no credential, audit or administration rights', 'org',
                ARRAY[
                    'agent:read', 'asset:read', 'assignment:read', 'ci:read',
                    'compliance:read', 'contact:read', 'contract:read', 'desk:read',
                    'discovery:read', 'disposal:read', 'document:read', 'entitlement:read',
                    'finding:read', 'form:read', 'iga:read', 'ipam:read',
                    'key:read', 'location:read', 'maintenance:read', 'monitoring:read',
                    'order:read', 'permission:read', 'rack:read', 'relationship:read',
                    'reservation:read', 'role:read', 'saved_view:read', 'search:read',
                    'sla:read', 'stock:read', 'stocktake:read', 'ticket:read',
                    'topology:read', 'training:read', 'user:read', 'webhook:read',
                    'workflow:read'
                ]),
            ('client_technician', 'Client-scoped technician: reads operational data and works tickets', 'client',
                ARRAY[
                    'alert:acknowledge', 'asset:read', 'assignment:read', 'ci:read',
                    'ci:write', 'contact:read', 'contact:write', 'contract:read',
                    'desk:read', 'document:read', 'export:run', 'ipam:read',
                    'job:read', 'key:read', 'location:read', 'maintenance:read',
                    'monitoring:read', 'order:read', 'rack:read', 'rack:write',
                    'relationship:read', 'relationship:write', 'reservation:read', 'review:resolve',
                    'saved_view:read', 'search:read', 'stock:read', 'stocktake:complete',
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
