-- WP-1071 (IPM-15, E-218): NAT translations.
--
-- * nat_translation(kind static|source|destination|pat, device_ci, inside
--   address or subnet, outside address or subnet, protocol, ports,
--   description): exactly one inside and one outside reference, ports only
--   with tcp/udp, pat needs the outside port. Composite foreign keys to
--   ci, ip_address and subnet (Konvention 19), all ON DELETE CASCADE: a
--   translation without its device or one side is meaningless.
-- * ip_address and subnet get UNIQUE (id, organization_id) when no unique
--   key over exactly these columns exists yet (another work package of
--   M3.13 may have created it first); the down migration drops it only
--   while no foreign key refers to it.
-- * client_id/site_id are derived from the device with the scope widened
--   (located-scope, pattern 000292); propagate_ci_scope (version of 000321)
--   also re-derives the translations of a CI whose client or site changes.
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
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
         WHERE c.conrelid = 'subnet'::regclass AND c.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)) = ARRAY['id', 'organization_id']) THEN
        ALTER TABLE subnet ADD CONSTRAINT subnet_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

CREATE TABLE nat_translation (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id          uuid NULL,
    site_id            uuid NULL,
    kind               text NOT NULL CONSTRAINT nat_translation_kind_check
                           CHECK (kind IN ('static', 'source', 'destination', 'pat')),
    device_ci_id       uuid NOT NULL,
    inside_address_id  uuid NULL,
    inside_subnet_id   uuid NULL,
    outside_address_id uuid NULL,
    outside_subnet_id  uuid NULL,
    protocol           text NOT NULL DEFAULT 'any' CONSTRAINT nat_translation_protocol_check
                           CHECK (protocol IN ('any', 'tcp', 'udp', 'icmp')),
    inside_port        integer NULL CONSTRAINT nat_translation_inside_port_check CHECK (inside_port BETWEEN 1 AND 65535),
    outside_port       integer NULL CONSTRAINT nat_translation_outside_port_check CHECK (outside_port BETWEEN 1 AND 65535),
    description        text NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT nat_translation_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT nat_translation_inside_check CHECK (num_nonnulls(inside_address_id, inside_subnet_id) = 1),
    CONSTRAINT nat_translation_outside_check CHECK (num_nonnulls(outside_address_id, outside_subnet_id) = 1),
    CONSTRAINT nat_translation_ports_check CHECK (
        (inside_port IS NULL AND outside_port IS NULL) OR protocol IN ('tcp', 'udp')),
    CONSTRAINT nat_translation_pat_port_check CHECK (kind <> 'pat' OR outside_port IS NOT NULL),
    CONSTRAINT nat_translation_device_fkey FOREIGN KEY (device_ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT nat_translation_inside_address_fkey FOREIGN KEY (inside_address_id, organization_id)
        REFERENCES ip_address (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT nat_translation_inside_subnet_fkey FOREIGN KEY (inside_subnet_id, organization_id)
        REFERENCES subnet (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT nat_translation_outside_address_fkey FOREIGN KEY (outside_address_id, organization_id)
        REFERENCES ip_address (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT nat_translation_outside_subnet_fkey FOREIGN KEY (outside_subnet_id, organization_id)
        REFERENCES subnet (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_nat_translation_device ON nat_translation (organization_id, device_ci_id);
CREATE INDEX idx_nat_translation_inside_address ON nat_translation (organization_id, inside_address_id)
    WHERE inside_address_id IS NOT NULL;
CREATE INDEX idx_nat_translation_inside_subnet ON nat_translation (organization_id, inside_subnet_id)
    WHERE inside_subnet_id IS NOT NULL;
CREATE INDEX idx_nat_translation_outside_address ON nat_translation (organization_id, outside_address_id)
    WHERE outside_address_id IS NOT NULL;
CREATE INDEX idx_nat_translation_outside_subnet ON nat_translation (organization_id, outside_subnet_id)
    WHERE outside_subnet_id IS NOT NULL;

-- The scope of a translation is that of its device, read with the scope
-- widened (000141) so a scoped writer cannot leave a stale scope; the
-- policies check the row afterwards.
CREATE FUNCTION derive_nat_translation_scope() RETURNS trigger
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

CREATE TRIGGER trg_nat_translation_scope
    BEFORE INSERT OR UPDATE ON nat_translation
    FOR EACH ROW EXECUTE FUNCTION derive_nat_translation_scope();
CREATE TRIGGER trg_nat_translation_updated_at
    BEFORE UPDATE ON nat_translation
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON nat_translation
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- propagate_ci_scope as of 000321, plus the NAT translations of the CI.
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
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

ALTER TABLE nat_translation ENABLE ROW LEVEL SECURITY;
ALTER TABLE nat_translation FORCE ROW LEVEL SECURITY;

-- Konvention 5 (located-scope): a scoped principal reads the translations
-- of its clients and sites and those without; it writes only translations
-- inside its scope.
CREATE POLICY nat_translation_isolation ON nat_translation
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
CREATE POLICY nat_translation_scoped_update ON nat_translation AS RESTRICTIVE FOR UPDATE
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
CREATE POLICY nat_translation_scoped_delete ON nat_translation AS RESTRICTIVE FOR DELETE
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
