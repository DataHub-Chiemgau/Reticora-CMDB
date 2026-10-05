-- Migration 000079 down: remove the restrictive write policies.

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT tablename, policyname
          FROM pg_policies
         WHERE schemaname = 'public' AND permissive = 'RESTRICTIVE'
           AND (policyname = tablename || '_scoped_update' OR policyname = tablename || '_scoped_delete')
    LOOP
        EXECUTE format('DROP POLICY %I ON public.%I', rec.policyname, rec.tablename);
    END LOOP;
END
$$;
