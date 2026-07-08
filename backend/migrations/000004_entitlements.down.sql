ALTER TABLE IF EXISTS entitlement DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS entitlement_isolation ON entitlement;

DROP INDEX IF EXISTS idx_entitlement_feature;
DROP INDEX IF EXISTS idx_entitlement_org;

DROP TABLE IF EXISTS entitlement;
