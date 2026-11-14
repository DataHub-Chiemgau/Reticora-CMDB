-- Migration 000286: permission keys of the network domain of phase 3
-- (WP-870, RBA-05, E-159 supplement network).
--
-- Four keys join the catalogue: credential:bind (binding credentials to CIs,
-- device groups, subnets and probes, CRD-01), vault:manage (vault
-- connections, CRD-02), admp:holder_read (the personal holder data of an
-- ADMP connection, ADM-05, SEC-05) and admp:manage (the ADMP accounts,
-- ADM-01). engineer gains credential:bind; the other three keys stay with
-- org_admin. client_technician and viewer hold no credential or ADMP key.

INSERT INTO permission (key, resource, action, description) VALUES
    ('credential:bind', 'credential', 'bind', 'Bind credentials to CIs, device groups, subnets and probes'),
    ('vault:manage', 'vault', 'manage', 'Manage vault connections for credential references'),
    ('admp:holder_read', 'admp', 'holder_read', 'Read the holder of an ADMP connection (personal data)'),
    ('admp:manage', 'admp', 'manage', 'Manage ADMP accounts')
ON CONFLICT (key) DO NOTHING;

-- The seed of migration 000285 with engineer holding credential:bind;
-- org_admin keeps the whole catalogue.
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
                    'contract:write', 'credential:bind', 'credential:manage', 'credential:read',
                    'desk:read', 'desk:write', 'discovery:ingest', 'discovery:manage',
                    'discovery:read', 'disposal:read', 'disposal:write', 'document:read',
                    'document:write', 'entitlement:read', 'export:run', 'finding:read',
                    'finding:write', 'form:read', 'form:write', 'iga:read',
                    'import:run', 'ipam:read', 'ipam:write', 'job:read',
                    'key:read', 'key:write', 'lifecycle:transition', 'location:read',
                    'location:write', 'maintenance:read', 'maintenance:write', 'monitoring:read',
                    'monitoring:write', 'order:read', 'order:write', 'override:write',
                    'permission:read', 'rack:read', 'rack:write', 'reconciliation:manage',
                    'relationship:read', 'relationship:write', 'reservation:read', 'reservation:write',
                    'review:resolve', 'role:read', 'saved_view:read', 'saved_view:write',
                    'search:read', 'search:write', 'sla:read', 'stock:read',
                    'stock:write', 'stocktake:complete', 'stocktake:read', 'stocktake:write',
                    'tag:manage', 'ticket:read', 'ticket:write', 'topology:read',
                    'training:read', 'training:write', 'user:read', 'vrf:manage',
                    'webhook:read', 'workflow:read', 'workflow:write'
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
