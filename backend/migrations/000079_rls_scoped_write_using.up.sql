-- Migration 000079: scoped principals may not change or delete org-wide rows
-- (TEN-05, found by the RLS matrix of WP-065).
--
-- The tenant policies follow TEN-05: USING shows org-wide rows (client_id,
-- site_id or team_id NULL) to a scoped principal, WITH CHECK requires the
-- scope for new rows. PostgreSQL applies only USING to DELETE and to the row
-- an UPDATE selects, so a client-scoped principal could delete an org-wide
-- CI or take it over by setting client_id to its own client. TEN-05 forbids
-- a scoped principal to create or change org-wide rows.
--
-- Every table whose single permissive ALL policy shows more than it lets
-- write gets two restrictive policies whose USING is that WITH CHECK
-- expression: one for UPDATE, one for DELETE. Reading stays as it is.

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT p.tablename, p.policyname, p.with_check
          FROM pg_policies p
         WHERE p.schemaname = 'public'
           AND p.permissive = 'PERMISSIVE'
           AND p.cmd = 'ALL'
           AND p.with_check IS NOT NULL
           AND p.qual IS DISTINCT FROM p.with_check
           AND NOT EXISTS (SELECT 1 FROM pg_policies o
                            WHERE o.schemaname = 'public' AND o.tablename = p.tablename
                              AND o.policyname <> p.policyname AND o.permissive = 'PERMISSIVE'
                              AND o.cmd IN ('ALL', 'UPDATE', 'DELETE'))
         ORDER BY p.tablename
    LOOP
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       rec.tablename || '_scoped_update', rec.tablename, rec.with_check, rec.with_check);
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR DELETE USING (%s)',
                       rec.tablename || '_scoped_delete', rec.tablename, rec.with_check);
    END LOOP;
END
$$;
