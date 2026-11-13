-- WP-1068 (IPM-17, E-217): BGP peerings as target inventory.
--
-- * bgp_peering(device, VRF, local ASN, remote AS number, local address,
--   remote address, expected_state, description); unique per device, VRF
--   and remote address. Composite foreign keys (Konvention 19): the device
--   (CASCADE), the VRF and the local ASN (RESTRICT), the local address
--   (SET NULL of local_address_id). The remote AS is stored as its number
--   and resolved against the asn table when read.
-- * ip_address gets UNIQUE (id, organization_id) when no unique key over
--   exactly these columns exists (WP-1071 may have created it); the down
--   migration drops it only while no foreign key refers to it.
-- * client_id/site_id are derived from the device with the scope widened
--   (located-scope); propagate_ci_scope (version of the NAT migration of
--   WP-1071) also re-derives the peerings of a CI.
-- * ENABLE and FORCE row level security with the policies of Konvention 5,
--   updated_at, TRUNCATE guard.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
         WHERE c.conrelid = 'ip_address'::regclass AND c.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)) = ARRAY['id', 'organization_id']) THEN
        ALTER TABLE ip_address ADD CONSTRAINT ip_address_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

CREATE TABLE bgp_peering (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id         uuid NULL,
    site_id           uuid NULL,
    device_ci_id      uuid NOT NULL,
    vrf_id            uuid NOT NULL,
    local_asn_id      uuid NULL,
    remote_asn_number bigint NOT NULL CONSTRAINT bgp_peering_remote_asn_check
                          CHECK (remote_asn_number BETWEEN 1 AND 4294967294),
    local_address_id  uuid NULL,
    remote_address    inet NOT NULL,
    expected_state    text NOT NULL DEFAULT 'established' CONSTRAINT bgp_peering_expected_state_check
                          CHECK (expected_state IN ('established', 'admin_down')),
    description       text NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT bgp_peering_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT bgp_peering_remote_key UNIQUE (organization_id, device_ci_id, vrf_id, remote_address),
    CONSTRAINT bgp_peering_device_fkey FOREIGN KEY (device_ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT bgp_peering_vrf_fkey FOREIGN KEY (vrf_id, organization_id)
        REFERENCES vrf (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT bgp_peering_local_asn_fkey FOREIGN KEY (local_asn_id, organization_id)
        REFERENCES asn (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT bgp_peering_local_address_fkey FOREIGN KEY (local_address_id, organization_id)
        REFERENCES ip_address (id, organization_id) ON DELETE SET NULL (local_address_id)
);
CREATE INDEX idx_bgp_peering_remote_asn ON bgp_peering (organization_id, remote_asn_number);
CREATE INDEX idx_bgp_peering_vrf ON bgp_peering (organization_id, vrf_id);
CREATE INDEX idx_bgp_peering_local_asn ON bgp_peering (organization_id, local_asn_id) WHERE local_asn_id IS NOT NULL;
CREATE INDEX idx_bgp_peering_local_address ON bgp_peering (organization_id, local_address_id) WHERE local_address_id IS NOT NULL;

-- The scope of a peering is that of its device, read with the scope
-- widened (000141); the policies check the row afterwards.
CREATE FUNCTION derive_bgp_peering_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
      FROM ci c WHERE c.id = NEW.device_ci_id AND c.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_bgp_peering_scope
    BEFORE INSERT OR UPDATE ON bgp_peering
    FOR EACH ROW EXECUTE FUNCTION derive_bgp_peering_scope();
CREATE TRIGGER trg_bgp_peering_updated_at
    BEFORE UPDATE ON bgp_peering
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON bgp_peering
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- propagate_ci_scope as of the NAT migration (WP-1071), plus the BGP
-- peerings of the CI.
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
    UPDATE ci_snapshot SET ci_id = ci_id WHERE ci_id = NEW.id;
    UPDATE ci_tag SET ci_id = ci_id WHERE ci_id = NEW.id;
    -- Addresses bound to the CI without interface (WP-911); those with an
    -- interface follow it (000141).
    UPDATE ip_address SET ci_id = ci_id
     WHERE organization_id = NEW.organization_id AND ci_id = NEW.id AND interface_id IS NULL;
    -- NAT translations of the device (WP-1071).
    UPDATE nat_translation SET device_ci_id = device_ci_id
     WHERE organization_id = NEW.organization_id AND device_ci_id = NEW.id;
    -- BGP peerings of the device (WP-1068).
    UPDATE bgp_peering SET device_ci_id = device_ci_id
     WHERE organization_id = NEW.organization_id AND device_ci_id = NEW.id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

ALTER TABLE bgp_peering ENABLE ROW LEVEL SECURITY;
ALTER TABLE bgp_peering FORCE ROW LEVEL SECURITY;

-- Konvention 5 (located-scope): a scoped principal reads the peerings of
-- its clients and sites and those without; it writes only peerings inside
-- its scope.
CREATE POLICY bgp_peering_isolation ON bgp_peering
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
CREATE POLICY bgp_peering_scoped_update ON bgp_peering AS RESTRICTIVE FOR UPDATE
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
CREATE POLICY bgp_peering_scoped_delete ON bgp_peering AS RESTRICTIVE FOR DELETE
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
