-- Revert Stage 5 permission and SLA objects.

DROP POLICY IF EXISTS ticket_sla_tenant_isolation ON ticket_sla;
DROP TABLE IF EXISTS ticket_sla;

DROP POLICY IF EXISTS sla_tenant_isolation ON sla;
DROP TABLE IF EXISTS sla;

DROP POLICY IF EXISTS role_permission_tenant_isolation ON role_permission;
DROP TABLE IF EXISTS role_permission;

DELETE FROM permission WHERE key IN ('ci:read', 'ci:write', 'ci:delete', 'topology:read', 'discovery:read', 'discovery:write', 'asset:read', 'asset:write', 'assignment:read', 'assignment:write', 'document:read', 'document:write', 'stocktake:read', 'stocktake:write', 'ticket:read', 'ticket:write', 'user:read', 'user:write', 'role:read', 'role:write', 'permission:read', 'permission:write', 'sla:read', 'sla:write', 'webhook:read', 'webhook:write', 'credential:read', 'credential:write', 'audit:read', 'ipam:read', 'ipam:write', 'rack:read', 'rack:write', 'contact:read', 'contact:write');
DROP TABLE IF EXISTS permission;
