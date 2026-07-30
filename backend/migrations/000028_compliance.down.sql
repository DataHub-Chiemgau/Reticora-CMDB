DROP POLICY IF EXISTS compliance_result_tenant_isolation ON compliance_result;
DROP TABLE IF EXISTS compliance_result;
DROP POLICY IF EXISTS compliance_rule_tenant_isolation ON compliance_rule;
DROP TABLE IF EXISTS compliance_rule;
DELETE FROM permission WHERE key IN ('compliance:read', 'compliance:write');
