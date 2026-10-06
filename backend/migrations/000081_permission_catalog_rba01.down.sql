-- Migration 000081 down: back to the catalogue of migration 000073.

INSERT INTO permission (key, resource, action, description) VALUES
    ('site:read', 'site', 'read', 'Read sites, buildings and rooms'),
    ('site:write', 'site', 'write', 'Manage sites, buildings and rooms'),
    ('discovery:write', 'discovery', 'write', 'Manage discovery jobs and imports'),
    ('reconciliation:resolve', 'reconciliation', 'resolve', 'Resolve reconciliation conflicts')
ON CONFLICT (key) DO NOTHING;

-- Translate every grant back; the keys new in 000081 are dropped. A
-- discovery:ingest that the up migration added to discovery:write holders
-- stays (it is a key of 000073 as well).
CREATE TEMP TABLE m081_map (old_key text, new_key text) ON COMMIT DROP;
INSERT INTO m081_map VALUES ('location:read', 'site:read'), ('location:write', 'site:write'),
    ('discovery:manage', 'discovery:write'), ('collector:manage', 'discovery:write'),
    ('review:resolve', 'reconciliation:resolve'), ('vrf:manage', NULL), ('job:read', NULL),
    ('notification:manage', NULL), ('team:manage', NULL);

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT rp.organization_id, rp.role_id, m.new_key
  FROM role_permission rp JOIN m081_map m ON m.old_key = rp.permission_key
 WHERE m.new_key IS NOT NULL
ON CONFLICT DO NOTHING;
DELETE FROM role_permission WHERE permission_key IN (SELECT old_key FROM m081_map);

UPDATE role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k) FILTER (WHERE k IS NOT NULL), '[]'::jsonb) FROM (
        SELECT CASE WHEN m.old_key IS NULL THEN e.k ELSE m.new_key END AS k
          FROM jsonb_array_elements_text(role.permissions) AS e(k)
          LEFT JOIN m081_map m ON m.old_key = e.k) t)
 WHERE permissions ?| (SELECT array_agg(old_key) FROM m081_map);

UPDATE custom_role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k) FILTER (WHERE k IS NOT NULL), '[]'::jsonb) FROM (
        SELECT CASE WHEN m.old_key IS NULL THEN e.k ELSE m.new_key END AS k
          FROM jsonb_array_elements_text(custom_role.permissions) AS e(k)
          LEFT JOIN m081_map m ON m.old_key = e.k) t)
 WHERE jsonb_typeof(permissions) = 'array' AND permissions ?| (SELECT array_agg(old_key) FROM m081_map);

UPDATE api_key SET permissions = ARRAY(
    SELECT DISTINCT CASE WHEN m.old_key IS NULL THEN k ELSE m.new_key END FROM unnest(api_key.permissions) AS k
      LEFT JOIN m081_map m ON m.old_key = k
     WHERE m.old_key IS NULL OR m.new_key IS NOT NULL ORDER BY 1)
 WHERE permissions && (SELECT array_agg(old_key) FROM m081_map);

DELETE FROM permission WHERE key IN (SELECT old_key FROM m081_map);

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

SELECT seed_standard_roles(id) FROM organization;
