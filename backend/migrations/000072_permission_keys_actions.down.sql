-- Reverts migration 000072.

DELETE FROM role_permission rp
USING role r
WHERE rp.role_id = r.id AND r.name = 'engineer' AND r.is_builtin
  AND rp.permission_key = 'ci_instance_attribute:manage';

DELETE FROM role_permission
WHERE permission_key IN ('credential:decrypt', 'lifecycle:transition', 'reconciliation:manage');

DELETE FROM permission
WHERE key IN ('credential:decrypt', 'lifecycle:transition', 'reconciliation:manage');
