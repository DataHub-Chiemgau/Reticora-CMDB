-- Migration 000276: the count of the CI list is an index-only scan again
-- (NFR-02, ABN-02; finding of the load run of PR #147).
--
-- idx_ci_list_sort (000156, WP-605) includes client_id and site_id, the
-- columns ci_isolation read, so the count of an unfiltered list (also the
-- capped count of E-104) was an index-only scan over the entries of the
-- organization. Since 000237 (WP-280, TEN-05) ci_isolation also reads
-- owner_team_id; the index lacks it, so every count reads the table (about
-- ten times the pages of the index at 10,000 CIs) and evaluates the policy
-- on the heap rows. The index includes owner_team_id now.
--
-- golang-migrate runs a file in one transaction, so CREATE INDEX
-- CONCURRENTLY is not possible here. The index keeps its name and is only
-- rebuilt while it lacks owner_team_id: with a large table the operator
-- builds the new definition beforehand under another name and swaps the
-- names (docs/schema-baseline.md, "Indizes ohne lange Sperren").
DO $$
BEGIN
    IF to_regclass('public.idx_ci_list_sort') IS NULL
       OR pg_get_indexdef('public.idx_ci_list_sort'::regclass) NOT LIKE '%INCLUDE (client_id, site_id, owner_team_id)%' THEN
        DROP INDEX IF EXISTS public.idx_ci_list_sort;
        CREATE INDEX idx_ci_list_sort ON public.ci (organization_id, created_at DESC, id DESC)
            INCLUDE (client_id, site_id, owner_team_id) WHERE deleted_at IS NULL;
    END IF;
END
$$;
