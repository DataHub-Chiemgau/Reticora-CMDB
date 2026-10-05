-- Migration 000081: permission catalogue after RBA-01 (WP-068).
--
-- The catalogue holds the keys of RBA-01 under their exact names: nine were
-- missing (location:read, location:write, discovery:manage, collector:manage,
-- vrf:manage, review:resolve, job:read, notification:manage, team:manage);
-- location:* replaces site:* (v2); the near-duplicates discovery:write and
-- reconciliation:resolve are replaced by the exact action keys. Module keys
-- of RBA-05/RBA-06 stay.
--
-- The standard roles follow RBA-02 for the new keys: engineer gains job:read
-- and vrf:manage, client_technician job:read (client scope), org_admin the
-- whole catalogue.

INSERT INTO permission (key, resource, action, description) VALUES
    ('location:read', 'location', 'read', 'Read the location tree: sites, buildings, rooms and racks'),
    ('location:write', 'location', 'write', 'Manage the location tree'),
    ('discovery:manage', 'discovery', 'manage', 'Manage discovery scopes and jobs'),
    ('collector:manage', 'collector', 'manage', 'Manage collectors and enrollment codes'),
    ('vrf:manage', 'vrf', 'manage', 'Manage VRFs'),
    ('review:resolve', 'review', 'resolve', 'Resolve review items'),
    ('job:read', 'job', 'read', 'Read jobs'),
    ('notification:manage', 'notification', 'manage', 'Manage notification channels'),
    ('team:manage', 'team', 'manage', 'Manage teams and memberships')
ON CONFLICT (key) DO NOTHING;

-- Translate every grant: role grants (role_permission, role.permissions),
-- custom roles and API keys. discovery:write gated collector management,
-- discovery jobs, the legacy ingest endpoint and collector heartbeats, so its
-- holders keep these abilities through the three exact keys.
CREATE TEMP TABLE m081_map (old_key text, new_key text) ON COMMIT DROP;
INSERT INTO m081_map VALUES ('site:read', 'location:read'), ('site:write', 'location:write'),
    ('discovery:write', 'discovery:manage'), ('discovery:write', 'collector:manage'),
    ('discovery:write', 'discovery:ingest'), ('reconciliation:resolve', 'review:resolve');

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT rp.organization_id, rp.role_id, m.new_key
  FROM role_permission rp JOIN m081_map m ON m.old_key = rp.permission_key
ON CONFLICT DO NOTHING;
DELETE FROM role_permission WHERE permission_key IN (SELECT old_key FROM m081_map);

UPDATE role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k), '[]'::jsonb) FROM (
        SELECT COALESCE(m.new_key, e.k) AS k
          FROM jsonb_array_elements_text(role.permissions) AS e(k)
          LEFT JOIN m081_map m ON m.old_key = e.k) t)
 WHERE permissions ?| (SELECT array_agg(old_key) FROM m081_map);

UPDATE custom_role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k), '[]'::jsonb) FROM (
        SELECT COALESCE(m.new_key, e.k) AS k
          FROM jsonb_array_elements_text(custom_role.permissions) AS e(k)
          LEFT JOIN m081_map m ON m.old_key = e.k) t)
 WHERE jsonb_typeof(permissions) = 'array' AND permissions ?| (SELECT array_agg(old_key) FROM m081_map);

UPDATE api_key SET permissions = ARRAY(
    SELECT DISTINCT COALESCE(m.new_key, k) FROM unnest(api_key.permissions) AS k
      LEFT JOIN m081_map m ON m.old_key = k ORDER BY 1)
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
