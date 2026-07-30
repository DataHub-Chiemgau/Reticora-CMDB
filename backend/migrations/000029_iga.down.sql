DROP POLICY IF EXISTS iga_drift_tenant_isolation ON iga_drift_finding;
DROP POLICY IF EXISTS iga_review_item_tenant_isolation ON iga_access_review_item;
DROP POLICY IF EXISTS iga_review_tenant_isolation ON iga_access_review;
DROP POLICY IF EXISTS iga_access_request_tenant_isolation ON iga_access_request;
DROP POLICY IF EXISTS iga_lifecycle_tenant_isolation ON iga_lifecycle_policy;
DROP POLICY IF EXISTS iga_task_tenant_isolation ON iga_provisioning_task;
DROP POLICY IF EXISTS iga_connector_tenant_isolation ON iga_connector;

DROP TABLE IF EXISTS iga_drift_finding;
DROP TABLE IF EXISTS iga_access_review_item;
DROP TABLE IF EXISTS iga_access_review;
DROP TABLE IF EXISTS iga_access_request;
DROP TABLE IF EXISTS iga_lifecycle_policy;
DROP TABLE IF EXISTS iga_provisioning_task;
DROP TABLE IF EXISTS iga_connector;

DELETE FROM permission WHERE key IN ('iga:read', 'iga:write');
