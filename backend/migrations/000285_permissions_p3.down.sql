-- Migration 000285 down: back to the catalogue and seed of migration 000192.
--
-- security:read and security:write return to the catalogue and the grants
-- are translated back from finding:*; the other keys new in 000285 are
-- removed from role_permission, role.permissions, custom_role.permissions
-- and api_key.permissions (NULL mapping), then from the catalogue.
-- seed_standard_roles is restored exactly as defined by 000192.

INSERT INTO permission (key, resource, action, description) VALUES
    ('security:read', 'security', 'read', 'Read security findings'),
    ('security:write', 'security', 'write', 'Manage security findings')
ON CONFLICT (key) DO NOTHING;

CREATE TEMP TABLE m285_map (old_key text, new_key text) ON COMMIT DROP;
INSERT INTO m285_map VALUES ('finding:read', 'security:read'), ('finding:write', 'security:write'),
    ('alert:acknowledge', NULL), ('workflow:approve', NULL), ('admp:read', NULL),
    ('escalation:manage', NULL), ('org:export', NULL), ('org:delete', NULL);

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT rp.organization_id, rp.role_id, m.new_key
  FROM role_permission rp JOIN m285_map m ON m.old_key = rp.permission_key
 WHERE m.new_key IS NOT NULL
ON CONFLICT DO NOTHING;
DELETE FROM role_permission WHERE permission_key IN (SELECT old_key FROM m285_map);

UPDATE role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k) FILTER (WHERE k IS NOT NULL), '[]'::jsonb) FROM (
        SELECT CASE WHEN m.old_key IS NULL THEN e.k ELSE m.new_key END AS k
          FROM jsonb_array_elements_text(role.permissions) AS e(k)
          LEFT JOIN m285_map m ON m.old_key = e.k) t)
 WHERE permissions ?| (SELECT array_agg(old_key) FROM m285_map);

UPDATE custom_role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k) FILTER (WHERE k IS NOT NULL), '[]'::jsonb) FROM (
        SELECT CASE WHEN m.old_key IS NULL THEN e.k ELSE m.new_key END AS k
          FROM jsonb_array_elements_text(custom_role.permissions) AS e(k)
          LEFT JOIN m285_map m ON m.old_key = e.k) t)
 WHERE jsonb_typeof(permissions) = 'array' AND permissions ?| (SELECT array_agg(old_key) FROM m285_map);

UPDATE api_key SET permissions = ARRAY(
    SELECT DISTINCT CASE WHEN m.old_key IS NULL THEN k ELSE m.new_key END FROM unnest(api_key.permissions) AS k
      LEFT JOIN m285_map m ON m.old_key = k
     WHERE m.old_key IS NULL OR m.new_key IS NOT NULL ORDER BY 1)
 WHERE permissions && (SELECT array_agg(old_key) FROM m285_map);

DELETE FROM permission WHERE key IN (SELECT old_key FROM m285_map);
DROP TABLE m285_map;

-- The seed of migration 000192, verbatim.
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
                    'agent:manage', 'agent:read', 'ai:read', 'asset:move',
                    'asset:read', 'asset:write', 'assignment:read', 'assignment:write',
                    'ci:delete', 'ci:read', 'ci:write', 'ci_instance_attribute:manage',
                    'collector:manage', 'compliance:read', 'compliance:write', 'contact:read',
                    'contact:write', 'contract:read', 'contract:write', 'credential:manage',
                    'credential:read', 'desk:read', 'desk:write', 'discovery:ingest',
                    'discovery:manage', 'discovery:read', 'disposal:read', 'disposal:write',
                    'document:read', 'document:write', 'entitlement:read', 'export:run',
                    'form:read', 'form:write', 'iga:read', 'import:run',
                    'ipam:read', 'ipam:write', 'job:read', 'key:read',
                    'key:write', 'lifecycle:transition', 'location:read', 'location:write',
                    'maintenance:read', 'maintenance:write', 'monitoring:read', 'monitoring:write',
                    'order:read', 'order:write', 'override:write', 'permission:read',
                    'rack:read', 'rack:write', 'reconciliation:manage', 'relationship:read',
                    'relationship:write', 'reservation:read', 'reservation:write', 'review:resolve',
                    'role:read', 'saved_view:read', 'saved_view:write', 'search:read',
                    'search:write', 'security:read', 'security:write', 'sla:read',
                    'stock:read', 'stock:write', 'stocktake:complete', 'stocktake:read',
                    'stocktake:write', 'tag:manage', 'ticket:read', 'ticket:write',
                    'topology:read', 'training:read', 'training:write', 'user:read',
                    'vrf:manage', 'webhook:read', 'workflow:read', 'workflow:write'
                ]),
            ('viewer', 'Read-only access across the organization; no credential, audit or administration rights', 'org',
                ARRAY[
                    'agent:read', 'asset:read', 'assignment:read', 'ci:read',
                    'compliance:read', 'contact:read', 'contract:read', 'desk:read',
                    'discovery:read', 'disposal:read', 'document:read', 'entitlement:read',
                    'form:read', 'iga:read', 'ipam:read', 'key:read',
                    'location:read', 'maintenance:read', 'monitoring:read', 'order:read',
                    'permission:read', 'rack:read', 'relationship:read', 'reservation:read',
                    'role:read', 'saved_view:read', 'search:read', 'security:read',
                    'sla:read', 'stock:read', 'stocktake:read', 'ticket:read',
                    'topology:read', 'training:read', 'user:read', 'webhook:read',
                    'workflow:read'
                ]),
            ('client_technician', 'Client-scoped technician: reads operational data and works tickets', 'client',
                ARRAY[
                    'asset:read', 'assignment:read', 'ci:read', 'ci:write',
                    'contact:read', 'contact:write', 'contract:read', 'desk:read',
                    'document:read', 'export:run', 'ipam:read', 'job:read',
                    'key:read', 'location:read', 'maintenance:read', 'monitoring:read',
                    'order:read', 'rack:read', 'rack:write', 'relationship:read',
                    'relationship:write', 'reservation:read', 'review:resolve', 'saved_view:read',
                    'search:read', 'stock:read', 'stocktake:complete', 'stocktake:read',
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

SELECT seed_standard_roles(id) FROM organization;
