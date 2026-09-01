DROP TABLE IF EXISTS endpoint_agent;
DELETE FROM permission WHERE key IN ('agent:read','agent:manage','agent:ingest');
