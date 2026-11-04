-- Reverts 000237: the policies of ci and asset without the team predicate
-- (000063/000151 with the scoped policies of 000079), no owner triggers,
-- no owner columns at ci. app_user_id_organization_key (000177) and
-- team_id_organization_key (000150) stay.

DROP POLICY ci_scoped_update ON ci;
DROP POLICY ci_scoped_delete ON ci;
DROP POLICY ci_isolation ON ci;
CREATE POLICY ci_isolation ON ci
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

DROP POLICY asset_scoped_update ON asset;
DROP POLICY asset_scoped_delete ON asset;
DROP POLICY asset_tenant_isolation ON asset;
CREATE POLICY asset_tenant_isolation ON asset
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT p.tablename, p.with_check
          FROM pg_policies p
         WHERE p.schemaname = 'public'
           AND (p.tablename, p.policyname) IN (('ci', 'ci_isolation'), ('asset', 'asset_tenant_isolation'))
         ORDER BY p.tablename
    LOOP
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       rec.tablename || '_scoped_update', rec.tablename, rec.with_check, rec.with_check);
        EXECUTE format('CREATE POLICY %I ON public.%I AS RESTRICTIVE FOR DELETE USING (%s)',
                       rec.tablename || '_scoped_delete', rec.tablename, rec.with_check);
    END LOOP;
END
$$;

DROP TRIGGER trg_asset_propagate_owner ON asset;
DROP FUNCTION asset_propagate_owner();
DROP TRIGGER trg_ci_asset_owner ON ci;
DROP FUNCTION ci_mirror_asset_owner();

ALTER TABLE ci
    DROP COLUMN owner_team_id,
    DROP COLUMN owner_user_id;
