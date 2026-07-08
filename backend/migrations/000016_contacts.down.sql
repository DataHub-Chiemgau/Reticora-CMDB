DROP INDEX IF EXISTS idx_ci_contact_contact;
DROP INDEX IF EXISTS idx_ci_contact_ci;
DROP INDEX IF EXISTS idx_contact_org;

DROP POLICY IF EXISTS org_isolation ON ci_contact;
DROP POLICY IF EXISTS org_isolation ON contact;

DROP TABLE IF EXISTS ci_contact;
DROP TABLE IF EXISTS contact;
