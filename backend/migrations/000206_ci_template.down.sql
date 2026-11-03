-- Migration 000206 down: CIs lose their template assignments; the policies,
-- the index, the scope trigger and the TRUNCATE guard go with the table.
DROP TRIGGER IF EXISTS ci_template_scope_propagation ON ci;
DROP TABLE IF EXISTS ci_template;
DROP FUNCTION IF EXISTS propagate_ci_template_scope();
