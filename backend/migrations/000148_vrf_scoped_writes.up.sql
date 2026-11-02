-- WP-372 (TEN-10, TEN-05, NET-02, NET-03; F-089, E-08, E-09): VRFs are
-- written only by org-wide principals.
--
-- A VRF is a routing domain of the whole organization (000092): it has no
-- client or site, and the subnets and addresses of every client lie in it.
-- Its only policy, vrf_isolation, checks the organization, so a principal
-- restricted to client 1 created VRFs, renamed them (the default "global"
-- included) and deleted them, changing the routing domains of every client.
--
-- Restrictive INSERT, UPDATE and DELETE policies require an empty client and
-- site scope, the org-wide marker of WithTenant (E-08), like
-- vlan_scoped_update and vlan_scoped_delete (000091) for org-wide VLANs
-- (E-09). For a scoped principal an INSERT fails WITH CHECK (42501, 403 at
-- the API), UPDATE and DELETE find no row (404). Reading stays with
-- vrf_isolation: subnets and addresses of every client reference the VRFs of
-- the organization. ENABLE and FORCE row level security stay. The default VRF
-- of a new organization (organization_default_vrf) is inserted in the
-- organization setup context, whose scopes are empty; the deletion of an
-- organization removes its VRFs through the foreign key cascade.

CREATE POLICY vrf_scoped_insert ON vrf AS RESTRICTIVE FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY vrf_scoped_update ON vrf AS RESTRICTIVE FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY vrf_scoped_delete ON vrf AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
