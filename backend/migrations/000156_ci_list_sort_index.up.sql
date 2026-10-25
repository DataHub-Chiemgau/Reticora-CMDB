-- WP-605 (NFR-02, ABN-02; F-117): index of the default sort of the CI list.
--
-- GET /cis sorts by created_at DESC, id DESC (ci.NormalizeSort), and no
-- index delivered that order: every page read all CIs of the organization,
-- evaluated the row level policy for each of them and sorted them, and the
-- count of the list (total) read the table once more. The nightly load test
-- missed the p95 budget of 500 ms for the list at 10,000 CIs.
--
-- idx_ci_list_sort holds the CIs of an organization in that order (an
-- ascending sort reads it backwards), so a page reads its first entries
-- only. client_id and site_id are included because ci_isolation (000063)
-- reads them: the count is then an index-only scan over the entries of the
-- organization instead of a read of the table.
--
-- golang-migrate runs a file in one transaction, so CREATE INDEX
-- CONCURRENTLY is not possible here. IF NOT EXISTS lets an operator build
-- the index beforehand without a long lock (docs/schema-baseline.md,
-- "Indizes ohne lange Sperren").

CREATE INDEX IF NOT EXISTS idx_ci_list_sort ON ci (organization_id, created_at DESC, id DESC)
    INCLUDE (client_id, site_id) WHERE deleted_at IS NULL;
