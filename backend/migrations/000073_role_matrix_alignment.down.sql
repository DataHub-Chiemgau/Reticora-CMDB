-- Reverts migration 000073: restores the seed function of migration 000032
-- and the grants the role matrix alignment removed or added.

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

-- Re-seeding restores role.permissions and the removed grants (viewer
-- credential:read and audit:read, engineer audit:read).
SELECT seed_standard_roles(id) FROM organization;

DELETE FROM role_permission rp
USING role r
WHERE rp.role_id = r.id AND r.is_builtin AND (
        (r.name = 'engineer' AND rp.permission_key IN ('credential:manage')) OR
        (r.name = 'client_technician' AND rp.permission_key IN ('ci:write', 'contact:write', 'export:run', 'rack:write', 'reconciliation:resolve', 'relationship:write'))
);
