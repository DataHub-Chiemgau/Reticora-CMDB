-- Migration 000285: permission keys of phase 3 after RBA-05 and the role
-- matrix of E-161 (WP-866, RBA-06).
--
-- finding:read and finding:write replace security:read and security:write
-- without an alias (RBA-05 names the module finding). Six keys join the
-- catalogue: alert:acknowledge (acknowledging alarms, incidents and
-- notification escalations), workflow:approve (approving workflow steps,
-- used by WP-1098), admp:read (reading ADMP connection data), and
-- escalation:manage, org:export and org:delete. Grants are translated in
-- role_permission, role.permissions (standard and organization roles),
-- custom_role.permissions and api_key.permissions before the replaced keys
-- leave the catalogue; role_permission.permission_key cascades on delete
-- (000026) and would drop the grants silently otherwise.
--
-- The standard roles follow E-161: org_admin holds the whole catalogue;
-- engineer gains alert:acknowledge and admp:read and holds finding:* instead
-- of security:*; viewer holds finding:read instead of security:read;
-- client_technician (client scope) gains alert:acknowledge. Only org_admin
-- holds workflow:approve, escalation:manage, org:export and org:delete;
-- other roles receive workflow:approve through custom roles.

-- (1) The new keys.
INSERT INTO permission (key, resource, action, description) VALUES
    ('finding:read', 'finding', 'read', 'Read security findings'),
    ('finding:write', 'finding', 'write', 'Manage security findings'),
    ('alert:acknowledge', 'alert', 'acknowledge', 'Acknowledge alarms, incidents and notification escalations'),
    ('workflow:approve', 'workflow', 'approve', 'Approve workflow steps'),
    ('admp:read', 'admp', 'read', 'Read ADMP connection data'),
    ('escalation:manage', 'escalation', 'manage', 'Manage escalation policies and notification escalations'),
    ('org:export', 'org', 'export', 'Export all data of the organization'),
    ('org:delete', 'org', 'delete', 'Request and cancel the deletion of the organization')
ON CONFLICT (key) DO NOTHING;

-- (2) Translate every grant.
CREATE TEMP TABLE m285_map (old_key text, new_key text) ON COMMIT DROP;
INSERT INTO m285_map VALUES ('security:read', 'finding:read'), ('security:write', 'finding:write');

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT rp.organization_id, rp.role_id, m.new_key
  FROM role_permission rp JOIN m285_map m ON m.old_key = rp.permission_key
ON CONFLICT DO NOTHING;
DELETE FROM role_permission WHERE permission_key IN (SELECT old_key FROM m285_map);

UPDATE role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k), '[]'::jsonb) FROM (
        SELECT COALESCE(m.new_key, e.k) AS k FROM jsonb_array_elements_text(role.permissions) AS e(k)
          LEFT JOIN m285_map m ON m.old_key = e.k) t)
 WHERE permissions ?| (SELECT array_agg(old_key) FROM m285_map);

UPDATE custom_role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k), '[]'::jsonb) FROM (
        SELECT COALESCE(m.new_key, e.k) AS k FROM jsonb_array_elements_text(custom_role.permissions) AS e(k)
          LEFT JOIN m285_map m ON m.old_key = e.k) t)
 WHERE jsonb_typeof(permissions) = 'array' AND permissions ?| (SELECT array_agg(old_key) FROM m285_map);

UPDATE api_key SET permissions = ARRAY(
    SELECT DISTINCT COALESCE(m.new_key, k) FROM unnest(api_key.permissions) AS k
      LEFT JOIN m285_map m ON m.old_key = k ORDER BY 1)
 WHERE permissions && (SELECT array_agg(old_key) FROM m285_map);

-- (3) Only now the replaced keys leave the catalogue.
DELETE FROM permission WHERE key IN (SELECT old_key FROM m285_map);
DROP TABLE m285_map;

-- (4) The seed of migration 000192 with the matrix of E-161; org_admin keeps
-- the whole catalogue.
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

-- Existing organizations: the seed replaces role.permissions and adds the
-- target grants to role_permission.
SELECT seed_standard_roles(id) FROM organization;
