-- WP-345 (TEN-02, TEN-05, IMP-07; F-050): the derived scope of edges and
-- child rows follows a CI moved by a client- or site-scoped principal.
--
-- The derivation and propagation triggers of migration 000061 run under the
-- RLS of the writer. When a principal restricted to clients 1 and 2 moved a
-- CI from client 1 to client 2, propagate_ci_scope did not see the edge to a
-- CI of client 3 and skipped it; derive_edge_scope would also have read the
-- invisible endpoint as NULL. The edge kept source_client_id = client 1, and
-- an observer of clients 1 and 3 saw an edge naming a CI it may not see. The
-- same held for sites.
--
-- propagate_ci_scope, propagate_interface_scope, propagate_subnet_scope and
-- derive_edge_scope now run with the client and site scope widened
-- (app_scope_widen/app_scope_restore, 000139): app.org_id stays, so they
-- reach every row of the organization and nothing beyond it;
-- derive_edge_scope reads only CIs of the row's organization
-- (relationship_suppression has no tenant foreign key). The triggers return
-- no rows to the caller. The policies of a row the caller writes are checked
-- after the BEFORE trigger restored the scope, so WITH CHECK is unchanged: a
-- scoped principal still writes no edge with an endpoint outside its scope
-- or without client (E-62, option 1 until approval). Only the rows re-derived
-- by a propagation trigger pass the policies with the widened scope.
-- search_path is fixed.

CREATE OR REPLACE FUNCTION derive_edge_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    SELECT c.client_id, c.site_id INTO NEW.source_client_id, NEW.source_site_id
      FROM ci c WHERE c.id = NEW.source_ci_id AND c.organization_id = NEW.organization_id;
    SELECT c.client_id, c.site_id INTO NEW.target_client_id, NEW.target_site_id
      FROM ci c WHERE c.id = NEW.target_ci_id AND c.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

-- A no-op UPDATE re-runs the derivation triggers of the dependent rows.
CREATE OR REPLACE FUNCTION propagate_ci_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE network_interface SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_contact SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_change SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_field_value SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_instance_field_definition SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE rack_mount SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE compliance_result SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE security_finding SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE discovery_result SET matched_ci_id = matched_ci_id WHERE matched_ci_id = NEW.id;
    UPDATE ci_relationship SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    UPDATE relationship_suppression SET source_ci_id = source_ci_id WHERE source_ci_id = NEW.id OR target_ci_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION propagate_interface_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ip_address SET interface_id = interface_id WHERE interface_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION propagate_subnet_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE ip_address SET subnet_id = subnet_id WHERE subnet_id = NEW.id AND interface_id IS NULL;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;
