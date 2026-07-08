-- Revert FORCE RLS and triggers

DROP TRIGGER IF EXISTS trg_collector_updated_at ON collector;
DROP TRIGGER IF EXISTS trg_app_user_updated_at ON app_user;
DROP TRIGGER IF EXISTS trg_ci_updated_at ON ci;
DROP TRIGGER IF EXISTS trg_ci_type_updated_at ON ci_type;
DROP TRIGGER IF EXISTS trg_rack_updated_at ON rack;
DROP TRIGGER IF EXISTS trg_room_updated_at ON room;
DROP TRIGGER IF EXISTS trg_building_updated_at ON building;
DROP TRIGGER IF EXISTS trg_site_updated_at ON site;
DROP TRIGGER IF EXISTS trg_client_updated_at ON client;
DROP TRIGGER IF EXISTS trg_organization_updated_at ON organization;

-- Revert policies to simple USING without WITH CHECK
DROP POLICY IF EXISTS rack_isolation ON rack;
CREATE POLICY rack_isolation ON rack
    USING (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS room_isolation ON room;
CREATE POLICY room_isolation ON room
    USING (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS building_isolation ON building;
CREATE POLICY building_isolation ON building
    USING (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS site_isolation ON site;
CREATE POLICY site_isolation ON site
    USING (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS client_isolation ON client;
CREATE POLICY client_isolation ON client
    USING (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS org_isolation ON organization;
CREATE POLICY org_isolation ON organization
    USING (id = current_setting('app.org_id')::UUID);

-- Remove FORCE (back to ENABLE only)
ALTER TABLE discovery_job NO FORCE ROW LEVEL SECURITY;
ALTER TABLE collector NO FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_delivery NO FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_subscription NO FORCE ROW LEVEL SECURITY;
ALTER TABLE role_assignment NO FORCE ROW LEVEL SECURITY;
ALTER TABLE role NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app_user NO FORCE ROW LEVEL SECURITY;
ALTER TABLE entitlement NO FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY;
ALTER TABLE ci_relationship NO FORCE ROW LEVEL SECURITY;
ALTER TABLE ci NO FORCE ROW LEVEL SECURITY;
ALTER TABLE ci_type NO FORCE ROW LEVEL SECURITY;
ALTER TABLE rack NO FORCE ROW LEVEL SECURITY;
ALTER TABLE room NO FORCE ROW LEVEL SECURITY;
ALTER TABLE building NO FORCE ROW LEVEL SECURITY;
ALTER TABLE site NO FORCE ROW LEVEL SECURITY;
ALTER TABLE client NO FORCE ROW LEVEL SECURITY;
ALTER TABLE organization NO FORCE ROW LEVEL SECURITY;

DROP FUNCTION IF EXISTS set_updated_at();
