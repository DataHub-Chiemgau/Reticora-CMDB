-- Migration 000073: role matrix alignment (WP-046, RBA-02)
--
-- The standard roles follow the RBA-02 matrix of the catalogue (E-01, E-13)
-- for the keys it governs, translated to the current permission keys:
--
--   ci:read, location:read (site:read, rack:read), topology:read
--       org_admin, engineer, viewer; client_technician in client scope (C)
--   ci:write, rack:write, relationship:write, contact:write
--       org_admin, engineer; client_technician (C)
--   ci:delete, location:write (site:write)            org_admin, engineer
--   review:resolve (reconciliation:resolve), export:run
--       org_admin, engineer; client_technician (C)
--   discovery:manage (discovery:write), credential:manage
--   (credential:read/write/manage/decrypt)              org_admin, engineer
--   citype:manage (citype:manage, ci_type:manage), webhook:manage
--                                                       org_admin
--   user:manage, role:manage (role:manage, permission:write),
--   entitlement:manage, audit:read, apikey:manage      org_admin
--
-- Keys the matrix does not govern (module permissions, RBA-05) keep their
-- grants. The viewer loses every credential right and audit:read, the
-- engineer audit:read; the client technician gains the writes marked C.
-- client_technician is a client-scope role: the session resolver grants it
-- only with a client scope (permission.PGRepository.AccessGrants).
--
-- seed_standard_roles now seeds new organizations with the same lists that
-- existing organizations hold, including the module grants of migrations
-- 000045 to 000072, which earlier only reached existing organizations.

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
                    'ci:write', 'ci_instance_attribute:manage', 'compliance:read', 'compliance:write',
                    'consumable:read', 'consumable:write', 'contact:read', 'contact:write',
                    'credential:decrypt', 'credential:manage', 'credential:read', 'credential:write',
                    'desk:read', 'desk:write', 'discovery:ingest', 'discovery:read',
                    'discovery:write', 'disposal:read', 'disposal:write', 'document:read',
                    'document:write', 'entitlement:read', 'export:run', 'form:read',
                    'form:write', 'iga:read', 'ipam:read', 'ipam:write',
                    'key:read', 'key:write', 'lifecycle:transition', 'maintenance:read',
                    'maintenance:write', 'monitoring:read', 'monitoring:write', 'order:read',
                    'order:write', 'override:write', 'permission:read', 'rack:read',
                    'rack:write', 'reconciliation:manage', 'reconciliation:resolve', 'relationship:read',
                    'relationship:write', 'role:read', 'saved_view:read', 'saved_view:write',
                    'search:read', 'search:write', 'security:read', 'security:write',
                    'site:read', 'site:write', 'sla:read', 'stocktake:read',
                    'stocktake:write', 'ticket:read', 'ticket:write', 'topology:read',
                    'training:read', 'training:write', 'user:read', 'webhook:read',
                    'workflow:read', 'workflow:write'
                ]),
            ('viewer', 'Read-only access across the organization; no credential, audit or administration rights', 'org',
                ARRAY[
                    'agent:read', 'asset:read', 'assignment:read', 'ci:read',
                    'compliance:read', 'consumable:read', 'contact:read', 'desk:read',
                    'discovery:read', 'disposal:read', 'document:read', 'entitlement:read',
                    'form:read', 'iga:read', 'ipam:read', 'key:read',
                    'maintenance:read', 'monitoring:read', 'order:read', 'permission:read',
                    'rack:read', 'relationship:read', 'role:read', 'saved_view:read',
                    'search:read', 'security:read', 'site:read', 'sla:read',
                    'stocktake:read', 'ticket:read', 'topology:read', 'training:read',
                    'user:read', 'webhook:read', 'workflow:read'
                ]),
            ('client_technician', 'Client-scoped technician: reads operational data and works tickets', 'client',
                ARRAY[
                    'asset:read', 'assignment:read', 'ci:read', 'ci:write',
                    'consumable:read', 'contact:read', 'contact:write', 'desk:read',
                    'document:read', 'export:run', 'ipam:read', 'key:read',
                    'maintenance:read', 'monitoring:read', 'order:read', 'rack:read',
                    'rack:write', 'reconciliation:resolve', 'relationship:read', 'relationship:write',
                    'saved_view:read', 'search:read', 'site:read', 'stocktake:read',
                    'stocktake:write', 'ticket:read', 'ticket:write', 'topology:read',
                    'training:read'
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

-- Existing organizations: the seed upserts role.permissions and adds the
-- target grants; grants the matrix denies are removed.
SELECT seed_standard_roles(id) FROM organization;

DELETE FROM role_permission rp
USING role r
WHERE rp.role_id = r.id AND r.is_builtin AND (
        (r.name = 'engineer' AND rp.permission_key IN ('apikey:manage', 'audit:read', 'ci_type:manage', 'citype:manage', 'entitlement:manage', 'permission:write', 'role:manage', 'user:manage', 'webhook:manage')) OR
        (r.name = 'viewer' AND rp.permission_key IN ('apikey:manage', 'audit:read', 'ci:delete', 'ci:write', 'ci_type:manage', 'citype:manage', 'contact:write', 'credential:decrypt', 'credential:manage', 'credential:read', 'credential:write', 'discovery:write', 'entitlement:manage', 'export:run', 'permission:write', 'rack:write', 'reconciliation:resolve', 'relationship:write', 'role:manage', 'site:write', 'user:manage', 'webhook:manage')) OR
        (r.name = 'client_technician' AND rp.permission_key IN ('apikey:manage', 'audit:read', 'ci:delete', 'ci_type:manage', 'citype:manage', 'credential:decrypt', 'credential:manage', 'credential:read', 'credential:write', 'discovery:write', 'entitlement:manage', 'permission:write', 'role:manage', 'site:write', 'user:manage', 'webhook:manage'))
);
