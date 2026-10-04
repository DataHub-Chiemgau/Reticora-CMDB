-- Migration 000072: permission keys for special actions (WP-045, RBA-04,
-- RBA-06, MET-14, CI-10)
--
-- The route authorization map now requires dedicated permissions for actions
-- that were reachable with a broader key: decrypting a credential (was
-- credential:read, held by viewers), lifecycle transitions of CIs and assets
-- (was ci:write / asset:write) and the reconciliation source policy (was
-- discovery:write). Instance attributes of a CI require
-- ci_instance_attribute:manage instead of ci:write.
--
-- org_admin receives every new key (seed_standard_roles grants org_admin the
-- whole catalogue for new organizations); engineer keeps the abilities it had
-- through the broader keys. viewer and client_technician get none of them.

INSERT INTO permission (key, resource, action, description) VALUES
    ('credential:decrypt', 'credential', 'decrypt', 'Decrypt stored credential secrets'),
    ('lifecycle:transition', 'lifecycle', 'transition', 'Execute lifecycle transitions of CIs and assets'),
    ('reconciliation:manage', 'reconciliation', 'manage', 'Manage reconciliation settings (source policy)')
ON CONFLICT (key) DO UPDATE SET
    resource = EXCLUDED.resource,
    action = EXCLUDED.action,
    description = EXCLUDED.description,
    updated_at = now();

INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, p.key
FROM role r
CROSS JOIN (VALUES
    ('credential:decrypt'), ('lifecycle:transition'), ('reconciliation:manage')
) AS p(key)
WHERE r.name IN ('org_admin', 'engineer') AND r.is_builtin
ON CONFLICT DO NOTHING;

-- Engineers edited instance attributes through ci:write so far.
INSERT INTO role_permission (organization_id, role_id, permission_key)
SELECT r.organization_id, r.id, 'ci_instance_attribute:manage'
FROM role r
WHERE r.name = 'engineer' AND r.is_builtin
ON CONFLICT DO NOTHING;
