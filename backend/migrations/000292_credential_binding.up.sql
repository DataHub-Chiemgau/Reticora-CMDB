-- Migration 000292: credential_binding binds a credential to a target for
-- one protocol namespace with a priority (CRD-01, WP-890, E-219). The
-- targets ci and subnet are usable; device_group and probe are reserved in
-- the CHECK of target_kind and get their columns, foreign keys and the
-- derivation of their scope with WP-935 (p3-probe-schema-def).
--
-- client_id and site_id are derived from the target (pattern 000061/000064,
-- with the scope widened like 000141) and follow a moved CI or subnet. A
-- credential of a client binds only to a target of the same client
-- (credential_binding_client_rule, 23514). The derived scope columns have no
-- foreign key (Konvention 19); credential, ci and subnet are referenced
-- together with organization_id.

-- UNIQUE (id, organization_id) on the targets (Konvention 19); since
-- migrations 000287/000288 they exist, the guards keep older states working.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'credential'::regclass AND conname = 'credential_id_organization_key') THEN
        ALTER TABLE credential ADD CONSTRAINT credential_id_organization_key UNIQUE (id, organization_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'subnet'::regclass AND conname = 'subnet_id_organization_key') THEN
        ALTER TABLE subnet ADD CONSTRAINT subnet_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

CREATE TABLE credential_binding (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    credential_id   uuid NOT NULL,
    target_kind     text NOT NULL CONSTRAINT credential_binding_target_kind_check
                        CHECK (target_kind IN ('ci', 'device_group', 'subnet', 'probe')),
    ci_id           uuid NULL,
    subnet_id       uuid NULL,
    namespace       text NOT NULL CONSTRAINT credential_binding_namespace_check
                        CHECK (namespace IN ('snmp', 'ssh', 'redfish', 'ipmi', 'wmi', 'api', 'nut', 'nas')),
    priority        integer NOT NULL DEFAULT 100 CONSTRAINT credential_binding_priority_check
                        CHECK (priority BETWEEN 0 AND 10000),
    client_id       uuid NULL,
    site_id         uuid NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    -- Exactly the column of the target kind is set; device_group and probe
    -- have none until WP-935.
    CONSTRAINT credential_binding_target_check CHECK (
        (target_kind = 'ci' AND ci_id IS NOT NULL AND subnet_id IS NULL)
        OR (target_kind = 'subnet' AND subnet_id IS NOT NULL AND ci_id IS NULL)
    ),
    CONSTRAINT credential_binding_credential_fkey FOREIGN KEY (credential_id, organization_id)
        REFERENCES credential (id, organization_id) ON DELETE RESTRICT,
    CONSTRAINT credential_binding_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT credential_binding_subnet_fkey FOREIGN KEY (subnet_id, organization_id)
        REFERENCES subnet (id, organization_id) ON DELETE CASCADE
);

CREATE INDEX idx_credential_binding_credential ON credential_binding (organization_id, credential_id);
CREATE UNIQUE INDEX uq_credential_binding_ci ON credential_binding (organization_id, ci_id, namespace, priority)
    WHERE ci_id IS NOT NULL;
CREATE UNIQUE INDEX uq_credential_binding_subnet ON credential_binding (organization_id, subnet_id, namespace, priority)
    WHERE subnet_id IS NOT NULL;

-- The scope of a binding is that of its target, read with the scope
-- widened (000141) so a scoped writer cannot leave a stale scope; the
-- policies check the row afterwards. A client credential binds only to a
-- target of its client.
CREATE FUNCTION derive_credential_binding_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    credential_client uuid;
BEGIN
    IF NEW.target_kind = 'ci' THEN
        SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
          FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    ELSIF NEW.target_kind = 'subnet' THEN
        SELECT s.client_id, s.site_id INTO NEW.client_id, NEW.site_id
          FROM subnet s WHERE s.id = NEW.subnet_id AND s.organization_id = NEW.organization_id;
    END IF;
    SELECT cr.client_id INTO credential_client
      FROM credential cr WHERE cr.id = NEW.credential_id AND cr.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    IF credential_client IS NOT NULL AND NEW.client_id IS DISTINCT FROM credential_client THEN
        RAISE EXCEPTION 'a credential of a client binds only to a target of the same client'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'credential_binding_client_rule';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_credential_binding_scope
    BEFORE INSERT OR UPDATE ON credential_binding
    FOR EACH ROW EXECUTE FUNCTION derive_credential_binding_scope();
CREATE TRIGGER trg_credential_binding_updated_at
    BEFORE UPDATE ON credential_binding
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON credential_binding
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- A moved CI or subnet re-derives the scope of its bindings (a no-op
-- UPDATE re-runs the derivation); a move that breaks the client rule of a
-- bound client credential fails.
CREATE FUNCTION propagate_credential_binding_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    IF TG_TABLE_NAME = 'ci' THEN
        UPDATE credential_binding SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    ELSE
        UPDATE credential_binding SET subnet_id = subnet_id WHERE subnet_id = NEW.id AND organization_id = NEW.organization_id;
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_ci_credential_binding_scope
    AFTER UPDATE OF client_id, site_id ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_credential_binding_scope();
CREATE TRIGGER trg_subnet_credential_binding_scope
    AFTER UPDATE OF client_id, site_id ON subnet
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_credential_binding_scope();

ALTER TABLE credential_binding ENABLE ROW LEVEL SECURITY;
ALTER TABLE credential_binding FORCE ROW LEVEL SECURITY;

-- Konvention 5: a scoped principal reads the bindings of its clients and
-- sites and those without; it writes only bindings inside its scope.
CREATE POLICY credential_binding_isolation ON credential_binding
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
CREATE POLICY credential_binding_scoped_update ON credential_binding AS RESTRICTIVE FOR UPDATE
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
CREATE POLICY credential_binding_scoped_delete ON credential_binding AS RESTRICTIVE FOR DELETE
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
