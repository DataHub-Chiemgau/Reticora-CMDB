-- Migration 000061: client and site of CI-dependent child tables (WP-025,
-- TEN-02, TEN-05, IMP-07, SRC-01)
--
-- TEN-02: tables below the client level carry client_id (NULL = org-wide),
-- tables with a location carry site_id denormalized. The child tables of a CI
-- only checked the organization, so a client-scoped principal saw and changed
-- interfaces, relationships, contacts, history, field values and findings of
-- every client.
--
-- 1. client_id and site_id are added and derived by a trigger from the
--    referenced CI on every INSERT and UPDATE; a writer cannot choose them.
--    ip_address follows its interface (or, without one, its subnet);
--    discovery_result follows its matched CI (or, unmatched, the collector of
--    its job). Relationships and suppressions reference two CIs and carry the
--    scope of both endpoints.
-- 2. When a CI, interface or subnet changes its client or site, the
--    dependent rows are re-derived. The propagation triggers have no column
--    list: an interface's client changes through its derivation trigger, not
--    through the SET list of the UPDATE.
-- 3. Existing rows are backfilled.
-- 4. The policies add the client predicate of migration 000058: USING keeps
--    org-wide rows (client_id IS NULL) readable, WITH CHECK requires a client
--    of the scope unless the scope is org-wide (E-09). A relationship is only
--    visible when both endpoints are (IMP-07). The site predicate follows with
--    WP-027 (CH25).

-- ─── 1. Columns ────────────────────────────────────────────────────────────────

ALTER TABLE network_interface ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE ip_address ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE ci_contact ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE ci_change ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE ci_field_value ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE ci_instance_field_definition ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE rack_mount ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE compliance_result ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE security_finding ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE discovery_result ADD COLUMN client_id UUID, ADD COLUMN site_id UUID;
ALTER TABLE ci_relationship
    ADD COLUMN source_client_id UUID, ADD COLUMN source_site_id UUID,
    ADD COLUMN target_client_id UUID, ADD COLUMN target_site_id UUID;
ALTER TABLE relationship_suppression
    ADD COLUMN source_client_id UUID, ADD COLUMN source_site_id UUID,
    ADD COLUMN target_client_id UUID, ADD COLUMN target_site_id UUID;

-- ─── 2. Derivation ─────────────────────────────────────────────────────────────

-- derive_ci_scope copies client_id and site_id of the CI named by the column
-- in TG_ARGV[0]; without a CI both stay NULL (org-wide).
CREATE FUNCTION derive_ci_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    ref UUID := (to_jsonb(NEW) ->> TG_ARGV[0])::uuid;
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF ref IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = ref;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION derive_edge_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT c.client_id, c.site_id INTO NEW.source_client_id, NEW.source_site_id FROM ci c WHERE c.id = NEW.source_ci_id;
    SELECT c.client_id, c.site_id INTO NEW.target_client_id, NEW.target_site_id FROM ci c WHERE c.id = NEW.target_ci_id;
    RETURN NEW;
END
$$;

CREATE FUNCTION derive_ip_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.interface_id IS NOT NULL THEN
        SELECT n.client_id, n.site_id INTO NEW.client_id, NEW.site_id FROM network_interface n WHERE n.id = NEW.interface_id;
    ELSIF NEW.subnet_id IS NOT NULL THEN
        SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id FROM subnet s WHERE s.id = NEW.subnet_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION derive_discovery_result_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    IF NEW.matched_ci_id IS NOT NULL THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id FROM ci c WHERE c.id = NEW.matched_ci_id;
    ELSE
        SELECT col.client_id INTO NEW.client_id
        FROM discovery_job j JOIN collector col ON col.id = j.collector_id
        WHERE j.id = NEW.job_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER network_interface_scope BEFORE INSERT OR UPDATE ON network_interface
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER ci_contact_scope BEFORE INSERT OR UPDATE ON ci_contact
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER ci_change_scope BEFORE INSERT OR UPDATE ON ci_change
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER ci_field_value_scope BEFORE INSERT OR UPDATE ON ci_field_value
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER ci_instance_field_definition_scope BEFORE INSERT OR UPDATE ON ci_instance_field_definition
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER rack_mount_scope BEFORE INSERT OR UPDATE ON rack_mount
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER compliance_result_scope BEFORE INSERT OR UPDATE ON compliance_result
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER security_finding_scope BEFORE INSERT OR UPDATE ON security_finding
    FOR EACH ROW EXECUTE FUNCTION derive_ci_scope('ci_id');
CREATE TRIGGER discovery_result_scope BEFORE INSERT OR UPDATE ON discovery_result
    FOR EACH ROW EXECUTE FUNCTION derive_discovery_result_scope();
CREATE TRIGGER ip_address_scope BEFORE INSERT OR UPDATE ON ip_address
    FOR EACH ROW EXECUTE FUNCTION derive_ip_scope();
CREATE TRIGGER ci_relationship_scope BEFORE INSERT OR UPDATE ON ci_relationship
    FOR EACH ROW EXECUTE FUNCTION derive_edge_scope();
CREATE TRIGGER relationship_suppression_scope BEFORE INSERT OR UPDATE ON relationship_suppression
    FOR EACH ROW EXECUTE FUNCTION derive_edge_scope();

-- ─── 3. Propagation ────────────────────────────────────────────────────────────

-- A no-op UPDATE re-runs the derivation triggers of the dependent rows.
CREATE FUNCTION propagate_ci_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
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
    RETURN NULL;
END
$$;

CREATE FUNCTION propagate_interface_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ip_address SET interface_id = interface_id WHERE interface_id = NEW.id;
    RETURN NULL;
END
$$;

CREATE FUNCTION propagate_subnet_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ip_address SET subnet_id = subnet_id WHERE subnet_id = NEW.id AND interface_id IS NULL;
    RETURN NULL;
END
$$;

CREATE TRIGGER ci_scope_propagation AFTER UPDATE ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_ci_scope();
CREATE TRIGGER network_interface_scope_propagation AFTER UPDATE ON network_interface
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_interface_scope();
CREATE TRIGGER subnet_scope_propagation AFTER UPDATE ON subnet
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_subnet_scope();

-- ─── 4. Backfill ───────────────────────────────────────────────────────────────

-- The derivation triggers fill the columns; network_interface runs before
-- ip_address, which reads it. None of these tables is append-only.
UPDATE network_interface SET ci_id = ci_id;
UPDATE ip_address SET subnet_id = subnet_id;
UPDATE ci_contact SET ci_id = ci_id;
UPDATE ci_change SET ci_id = ci_id;
UPDATE ci_field_value SET ci_id = ci_id;
UPDATE ci_instance_field_definition SET ci_id = ci_id;
UPDATE rack_mount SET ci_id = ci_id;
UPDATE compliance_result SET ci_id = ci_id;
UPDATE security_finding SET ci_id = ci_id;
UPDATE discovery_result SET matched_ci_id = matched_ci_id;
UPDATE ci_relationship SET source_ci_id = source_ci_id;
UPDATE relationship_suppression SET source_ci_id = source_ci_id;

CREATE INDEX idx_network_interface_client ON network_interface (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_ip_address_client ON ip_address (client_id) WHERE client_id IS NOT NULL;
CREATE INDEX idx_ci_relationship_clients ON ci_relationship (source_client_id, target_client_id);

-- ─── 5. Policies ───────────────────────────────────────────────────────────────

DO $$
DECLARE
    entry RECORD;
    scope_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('network_interface', 'org_isolation'),
            ('ip_address', 'org_isolation'),
            ('ci_contact', 'org_isolation'),
            ('ci_change', 'org_isolation'),
            ('ci_field_value', 'ci_field_value_isolation'),
            ('ci_instance_field_definition', 'ci_instance_field_definition_isolation'),
            ('rack_mount', 'rack_mount_isolation'),
            ('compliance_result', 'compliance_result_tenant_isolation'),
            ('security_finding', 'security_finding_isolation'),
            ('discovery_result', 'discovery_result_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (%s AND (%s OR client_id IS NULL OR %s)) '
            'WITH CHECK (%s AND (%s OR %s))',
            entry.policy_name, entry.table_name,
            org_match, scope_all, 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])',
            org_match, scope_all, 'client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])'
        );
    END LOOP;

    FOR entry IN
        SELECT * FROM (VALUES
            ('ci_relationship', 'ci_relationship_isolation'),
            ('relationship_suppression', 'suppression_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (%s AND (%s OR ('
                '(source_client_id IS NULL OR source_client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])) '
                'AND (target_client_id IS NULL OR target_client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]))))) '
            'WITH CHECK (%s AND (%s OR ('
                'source_client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]) '
                'AND target_client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]))))',
            entry.policy_name, entry.table_name,
            org_match, scope_all,
            org_match, scope_all
        );
    END LOOP;
END
$$;
