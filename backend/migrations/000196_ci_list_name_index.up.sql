-- WP-839 (NFR-02, NFR-01; E-17, E-104): index of the CI list sorted by name.
--
-- GET /cis?sort_by=name sorts by name, id (ci.NormalizeSort), and no index
-- delivered that order: every page read all CIs of the organization,
-- evaluated the row level policy for each of them and sorted them. With
-- idx_ci_list_name a page reads its first entries only (a descending sort
-- reads it backwards). The default sort has idx_ci_list_sort (000156,
-- WP-605); updated_at and status get an index only when the load test
-- (tests/load/api.js, scenario api-large) asks for one.
--
-- golang-migrate runs a file in one transaction, so CREATE INDEX
-- CONCURRENTLY is not possible here. IF NOT EXISTS lets an operator build
-- the index beforehand without a long lock (docs/schema-baseline.md,
-- "Indizes ohne lange Sperren").

CREATE INDEX IF NOT EXISTS idx_ci_list_name ON ci (organization_id, name, id) WHERE deleted_at IS NULL;
