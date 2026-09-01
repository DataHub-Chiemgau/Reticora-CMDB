DROP TABLE IF EXISTS security_finding;
DELETE FROM permission WHERE key IN ('security:read','security:write');
