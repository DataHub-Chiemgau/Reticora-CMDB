-- Migration 000147: RBA-01 key clean-up (WP-363).
--
-- ci_type:manage was a near-duplicate of the RBA-01 key citype:manage and the
-- key the CI-type routes required; the routes now require citype:manage, so
-- every grant of ci_type:manage becomes citype:manage. The keys that gate no
-- route are retired (F-199): credential:decrypt (its endpoint was removed by
-- WP-047) and the unused keys asset:assign, credential:write,
-- permission:write, role:write, user:write and webhook:write, whose actions
-- run under asset:write, credential:manage, role:manage, user:manage and
-- webhook:manage. Removing them changes no effective right. Grants are
-- rewritten in role_permission, role.permissions (standard and organization
-- roles), custom_role.permissions and api_key.permissions. entitlement:manage
-- stays: RBA-01 names it and org_admin holds it, but no tenant route requires
-- it (ENT-04, F-211).

CREATE TEMP TABLE m147_map (old_key text, new_key text) ON COMMIT DROP;
INSERT INTO m147_map VALUES ('ci_type:manage', 'citype:manage'), ('credential:decrypt', NULL),
    ('asset:assign', NULL), ('credential:write', NULL), ('permission:write', NULL),
    ('role:write', NULL), ('user:write', NULL), ('webhook:write', NULL);

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT rp.organization_id, rp.role_id, m.new_key
  FROM role_permission rp JOIN m147_map m ON m.old_key = rp.permission_key
 WHERE m.new_key IS NOT NULL
ON CONFLICT DO NOTHING;
DELETE FROM role_permission WHERE permission_key IN (SELECT old_key FROM m147_map);

UPDATE role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k) FILTER (WHERE k IS NOT NULL), '[]'::jsonb) FROM (
        SELECT CASE WHEN m.old_key IS NULL THEN e.k ELSE m.new_key END AS k
          FROM jsonb_array_elements_text(role.permissions) AS e(k)
          LEFT JOIN m147_map m ON m.old_key = e.k) t)
 WHERE permissions ?| (SELECT array_agg(old_key) FROM m147_map);

UPDATE custom_role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k) FILTER (WHERE k IS NOT NULL), '[]'::jsonb) FROM (
        SELECT CASE WHEN m.old_key IS NULL THEN e.k ELSE m.new_key END AS k
          FROM jsonb_array_elements_text(custom_role.permissions) AS e(k)
          LEFT JOIN m147_map m ON m.old_key = e.k) t)
 WHERE jsonb_typeof(permissions) = 'array' AND permissions ?| (SELECT array_agg(old_key) FROM m147_map);

UPDATE api_key SET permissions = ARRAY(
    SELECT DISTINCT CASE WHEN m.old_key IS NULL THEN k ELSE m.new_key END FROM unnest(api_key.permissions) AS k
      LEFT JOIN m147_map m ON m.old_key = k
     WHERE m.old_key IS NULL OR m.new_key IS NOT NULL ORDER BY 1)
 WHERE permissions && (SELECT array_agg(old_key) FROM m147_map);

DELETE FROM permission WHERE key IN (SELECT old_key FROM m147_map);

-- The seed of migration 000081 without the retired keys (engineer: asset:assign,
-- credential:decrypt, credential:write); org_admin keeps the whole catalogue.
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
                    'asset:read', 'asset:reserve', 'asset:write', 'assignment:read',
                    'assignment:write', 'ci:delete', 'ci:read', 'ci:write',
                    'ci_instance_attribute:manage', 'collector:manage', 'compliance:read', 'compliance:write',
                    'consumable:read', 'consumable:write', 'contact:read', 'contact:write',
                    'credential:manage', 'credential:read', 'desk:read', 'desk:write',
                    'discovery:ingest', 'discovery:manage', 'discovery:read', 'disposal:read',
                    'disposal:write', 'document:read', 'document:write', 'entitlement:read',
                    'export:run', 'form:read', 'form:write', 'iga:read',
                    'ipam:read', 'ipam:write', 'job:read', 'key:read',
                    'key:write', 'lifecycle:transition', 'location:read', 'location:write',
                    'maintenance:read', 'maintenance:write', 'monitoring:read', 'monitoring:write',
                    'order:read', 'order:write', 'override:write', 'permission:read',
                    'rack:read', 'rack:write', 'reconciliation:manage', 'relationship:read',
                    'relationship:write', 'review:resolve', 'role:read', 'saved_view:read',
                    'saved_view:write', 'search:read', 'search:write', 'security:read',
                    'security:write', 'sla:read', 'stocktake:read', 'stocktake:write',
                    'ticket:read', 'ticket:write', 'topology:read', 'training:read',
                    'training:write', 'user:read', 'vrf:manage', 'webhook:read',
                    'workflow:read', 'workflow:write'
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

