-- Migration 000192: permission keys of phase 2 after RBA-05 and the role
-- matrix of E-106 (WP-245, RBA-06).
--
-- Thirteen keys join the catalogue: the module keys stock:*, reservation:*
-- and contract:* of RBA-05 and the action keys stocktake:complete,
-- import:run, import:manage, field_permission:manage, tag:manage,
-- metamodel:publish and saved_view:manage. Four keys are replaced without an
-- alias: consumable:read -> stock:read, consumable:write -> stock:write,
-- inventory:manage -> stock:write and asset:reserve -> reservation:write.
-- Two keys are complemented, the former key stays: asset:read gains
-- reservation:read (the reservations route read with asset:read) and
-- stocktake:write gains stocktake:complete (POST /stocktakes/{id}/complete
-- required stocktake:write). Grants are translated in role_permission,
-- role.permissions (standard and organization roles),
-- custom_role.permissions and api_key.permissions before the replaced keys
-- leave the catalogue; role_permission.permission_key cascades on delete
-- (000026) and would drop the grants silently otherwise.
--
-- The standard roles follow E-106: org_admin holds the whole catalogue;
-- engineer gains stock:*, reservation:*, contract:*, stocktake:complete,
-- import:run and tag:manage; viewer stock:read, reservation:read and
-- contract:read; client_technician (client scope) stock:read,
-- reservation:read, contract:read and stocktake:complete. Only org_admin
-- holds metamodel:publish, field_permission:manage, import:manage,
-- saved_view:manage, lifecycle:manage and relationship_type:manage.
--
-- Consequences (E-106): engineer writes inventory/items through stock:write,
-- which only org_admin could through inventory:manage; custom roles and API
-- keys holding asset:read only lose reading inventory/items and
-- inventory/availability, which require stock:read now. Custom roles with
-- asset:write or order:write but without contract:read do not gain it.

-- (1) The new keys.
INSERT INTO permission (key, resource, action, description) VALUES
    ('stock:read', 'stock', 'read', 'Read consumables, quantity items and stock'),
    ('stock:write', 'stock', 'write', 'Manage consumables, quantity items and stock'),
    ('reservation:read', 'reservation', 'read', 'Read reservations'),
    ('reservation:write', 'reservation', 'write', 'Create and release reservations'),
    ('contract:read', 'contract', 'read', 'Read contracts and suppliers'),
    ('contract:write', 'contract', 'write', 'Manage contracts and suppliers'),
    ('stocktake:complete', 'stocktake', 'complete', 'Complete stocktakes'),
    ('import:run', 'import', 'run', 'Run imports and read import runs and mapping profiles'),
    ('import:manage', 'import', 'manage', 'Manage import mapping profiles'),
    ('field_permission:manage', 'field_permission', 'manage', 'Manage field permissions'),
    ('tag:manage', 'tag', 'manage', 'Manage tags'),
    ('metamodel:publish', 'metamodel', 'publish', 'Publish metamodel drafts and template versions'),
    ('saved_view:manage', 'saved_view', 'manage', 'Manage saved views of other owners')
ON CONFLICT (key) DO NOTHING;

-- (2) Translate every grant. keep marks a complementing mapping: the new key
-- is added and the former key stays (union instead of replacement).
CREATE TEMP TABLE m192_map (old_key text, new_key text, keep boolean) ON COMMIT DROP;
INSERT INTO m192_map VALUES
    ('consumable:read', 'stock:read', false), ('consumable:write', 'stock:write', false),
    ('inventory:manage', 'stock:write', false), ('asset:reserve', 'reservation:write', false),
    ('asset:read', 'reservation:read', true), ('stocktake:write', 'stocktake:complete', true);

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT rp.organization_id, rp.role_id, m.new_key
  FROM role_permission rp JOIN m192_map m ON m.old_key = rp.permission_key
ON CONFLICT DO NOTHING;
DELETE FROM role_permission WHERE permission_key IN (SELECT old_key FROM m192_map WHERE NOT keep);

UPDATE role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k), '[]'::jsonb) FROM (
        SELECT e.k FROM jsonb_array_elements_text(role.permissions) AS e(k)
         WHERE e.k NOT IN (SELECT old_key FROM m192_map WHERE NOT keep)
        UNION
        SELECT m.new_key FROM jsonb_array_elements_text(role.permissions) AS e(k)
          JOIN m192_map m ON m.old_key = e.k) t)
 WHERE permissions ?| (SELECT array_agg(old_key) FROM m192_map);

UPDATE custom_role SET permissions = (
    SELECT COALESCE(jsonb_agg(DISTINCT k ORDER BY k), '[]'::jsonb) FROM (
        SELECT e.k FROM jsonb_array_elements_text(custom_role.permissions) AS e(k)
         WHERE e.k NOT IN (SELECT old_key FROM m192_map WHERE NOT keep)
        UNION
        SELECT m.new_key FROM jsonb_array_elements_text(custom_role.permissions) AS e(k)
          JOIN m192_map m ON m.old_key = e.k) t)
 WHERE jsonb_typeof(permissions) = 'array' AND permissions ?| (SELECT array_agg(old_key) FROM m192_map);

UPDATE api_key SET permissions = ARRAY(
    SELECT k FROM (
        SELECT k FROM unnest(api_key.permissions) AS k
         WHERE k NOT IN (SELECT old_key FROM m192_map WHERE NOT keep)
        UNION
        SELECT m.new_key FROM unnest(api_key.permissions) AS k
          JOIN m192_map m ON m.old_key = k) t ORDER BY 1)
 WHERE permissions && (SELECT array_agg(old_key) FROM m192_map);

-- (3) Only now the replaced keys leave the catalogue.
DELETE FROM permission WHERE key IN (SELECT old_key FROM m192_map WHERE NOT keep);

-- (4) The seed of migration 000147 with the matrix of E-106; org_admin keeps
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

-- Existing organizations: the seed replaces role.permissions and adds the
-- target grants to role_permission. The seed only adds there, so the grants
-- E-106 denies a standard role are removed explicitly (pattern of 000073):
-- the keys only org_admin holds, and the phase-2 keys a role does not hold,
-- which the translation can only have produced from grants added to a
-- standard role beyond its seed.
SELECT seed_standard_roles(id) FROM organization;

DELETE FROM role_permission rp
USING role r
WHERE rp.role_id = r.id AND r.is_builtin AND (
        (r.name IN ('engineer', 'viewer', 'client_technician') AND rp.permission_key IN (
            'metamodel:publish', 'field_permission:manage', 'import:manage', 'saved_view:manage',
            'lifecycle:manage', 'relationship_type:manage')) OR
        (r.name IN ('viewer', 'client_technician') AND rp.permission_key IN (
            'stock:write', 'reservation:write', 'contract:write', 'import:run', 'tag:manage')) OR
        (r.name = 'viewer' AND rp.permission_key = 'stocktake:complete')
);
