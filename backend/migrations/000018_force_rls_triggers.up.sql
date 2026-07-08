-- Add FORCE ROW LEVEL SECURITY and set_updated_at trigger
-- Fixes: FORCE RLS missing, updated_at trigger missing, WITH CHECK clauses

-- Global updated_at trigger function
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- FORCE RLS on all tenant tables
ALTER TABLE organization FORCE ROW LEVEL SECURITY;
ALTER TABLE client FORCE ROW LEVEL SECURITY;
ALTER TABLE site FORCE ROW LEVEL SECURITY;
ALTER TABLE building FORCE ROW LEVEL SECURITY;
ALTER TABLE room FORCE ROW LEVEL SECURITY;
ALTER TABLE rack FORCE ROW LEVEL SECURITY;
ALTER TABLE ci_type FORCE ROW LEVEL SECURITY;
ALTER TABLE ci FORCE ROW LEVEL SECURITY;
ALTER TABLE ci_relationship FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
ALTER TABLE entitlement FORCE ROW LEVEL SECURITY;
ALTER TABLE app_user FORCE ROW LEVEL SECURITY;
ALTER TABLE role FORCE ROW LEVEL SECURITY;
ALTER TABLE role_assignment FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_subscription FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_delivery FORCE ROW LEVEL SECURITY;
ALTER TABLE collector FORCE ROW LEVEL SECURITY;
ALTER TABLE discovery_job FORCE ROW LEVEL SECURITY;

-- Add WITH CHECK to existing policies (drop and recreate)
DROP POLICY IF EXISTS org_isolation ON organization;
CREATE POLICY org_isolation ON organization
    USING (id = current_setting('app.org_id')::UUID)
    WITH CHECK (id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS client_isolation ON client;
CREATE POLICY client_isolation ON client
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS site_isolation ON site;
CREATE POLICY site_isolation ON site
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS building_isolation ON building;
CREATE POLICY building_isolation ON building
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS room_isolation ON room;
CREATE POLICY room_isolation ON room
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

DROP POLICY IF EXISTS rack_isolation ON rack;
CREATE POLICY rack_isolation ON rack
    USING (organization_id = current_setting('app.org_id')::UUID)
    WITH CHECK (organization_id = current_setting('app.org_id')::UUID);

-- Apply set_updated_at trigger to all tables with updated_at
CREATE TRIGGER trg_organization_updated_at BEFORE UPDATE ON organization
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_client_updated_at BEFORE UPDATE ON client
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_site_updated_at BEFORE UPDATE ON site
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_building_updated_at BEFORE UPDATE ON building
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_room_updated_at BEFORE UPDATE ON room
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_rack_updated_at BEFORE UPDATE ON rack
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ci_type_updated_at BEFORE UPDATE ON ci_type
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ci_updated_at BEFORE UPDATE ON ci
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_app_user_updated_at BEFORE UPDATE ON app_user
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_collector_updated_at BEFORE UPDATE ON collector
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Rename RLS variable from app.organization_id to app.org_id in original policies
-- (This migration assumes fresh apply or that the variables have been aligned)
