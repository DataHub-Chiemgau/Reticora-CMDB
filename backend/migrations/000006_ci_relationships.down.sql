ALTER TABLE IF EXISTS ci_relationship DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS ci_relationship_isolation ON ci_relationship;

DROP INDEX IF EXISTS idx_ci_rel_type;
DROP INDEX IF EXISTS idx_ci_rel_target;
DROP INDEX IF EXISTS idx_ci_rel_source;
DROP INDEX IF EXISTS idx_ci_rel_org;

DROP TABLE IF EXISTS ci_relationship;
