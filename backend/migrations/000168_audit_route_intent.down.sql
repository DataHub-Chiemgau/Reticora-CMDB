-- Migration audit_route_intent down: no audit intents; the policies, the
-- index and the TRUNCATE guard go with the table. Pending intents are lost.
DROP TABLE IF EXISTS audit_route_intent;
