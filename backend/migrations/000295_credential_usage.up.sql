-- Migration 000295: credential_usage records per credential, CI and protocol
-- namespace when a collector last used the credential successfully and when
-- it last failed, with the error class (CRD-06, WP-898). The rows come from
-- the scope runs the collectors report (never with a secret); a later
-- producer (probe results, WP-960) writes through the same upsert.
--
-- client_id and site_id are derived from the CI (located-scope, pattern
-- 000061/000292, the scope widened like 000141) and follow a moved CI. The
-- derived scope columns have no foreign key (Konvention 19); credential and
-- ci are referenced together with organization_id. The table is no
-- append-only ledger: one row per key is updated in place.

CREATE TABLE credential_usage (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    credential_id    uuid NOT NULL,
    ci_id            uuid NOT NULL,
    namespace        text NOT NULL CONSTRAINT credential_usage_namespace_check
                         CHECK (namespace IN ('snmp', 'ssh', 'redfish', 'ipmi', 'wmi', 'api', 'nut', 'nas')),
    last_success_at  timestamptz NULL,
    last_failure_at  timestamptz NULL,
    last_error_class text NULL CONSTRAINT credential_usage_error_class_check
                         CHECK (last_error_class IN ('auth_failed', 'timeout', 'unreachable', 'tls',
                             'vault_unreachable', 'vault_denied', 'vault_not_found', 'field_missing')),
    -- When the last credential.resolution_failed of the key was written
    -- (at most one per hour, AK4).
    last_event_at    timestamptz NULL,
    client_id        uuid NULL,
    site_id          uuid NULL,
    CONSTRAINT credential_usage_key UNIQUE (credential_id, ci_id, namespace),
    CONSTRAINT credential_usage_credential_fkey FOREIGN KEY (credential_id, organization_id)
        REFERENCES credential (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT credential_usage_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE
);

CREATE INDEX idx_credential_usage_ci ON credential_usage (organization_id, ci_id);

-- The scope of a usage row is that of its CI, read with the scope widened
-- (000141) so the system writer of a scoped run never leaves a stale scope.
CREATE FUNCTION derive_credential_usage_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    SELECT c.client_id, c.site_id INTO NEW.client_id, NEW.site_id
      FROM ci c WHERE c.id = NEW.ci_id AND c.organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_credential_usage_scope
    BEFORE INSERT OR UPDATE OF ci_id ON credential_usage
    FOR EACH ROW EXECUTE FUNCTION derive_credential_usage_scope();
CREATE TRIGGER reticora_truncate_guard
    BEFORE TRUNCATE ON credential_usage
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- A moved CI re-derives the scope of its usage rows (a no-op UPDATE of
-- ci_id re-runs the derivation).
CREATE FUNCTION propagate_credential_usage_scope() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
BEGIN
    UPDATE credential_usage SET ci_id = ci_id WHERE ci_id = NEW.id AND organization_id = NEW.organization_id;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_ci_credential_usage_scope
    AFTER UPDATE OF client_id, site_id ON ci
    FOR EACH ROW WHEN (OLD.client_id IS DISTINCT FROM NEW.client_id OR OLD.site_id IS DISTINCT FROM NEW.site_id)
    EXECUTE FUNCTION propagate_credential_usage_scope();

ALTER TABLE credential_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE credential_usage FORCE ROW LEVEL SECURITY;

-- Konvention 5: a scoped principal reads the usage of the CIs of its
-- clients and sites and of CIs without; it writes only inside its scope.
CREATE POLICY credential_usage_isolation ON credential_usage
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
CREATE POLICY credential_usage_scoped_update ON credential_usage AS RESTRICTIVE FOR UPDATE
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
CREATE POLICY credential_usage_scoped_delete ON credential_usage AS RESTRICTIVE FOR DELETE
    USING (
        organization_id = current_setting('app.org_id')::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );
