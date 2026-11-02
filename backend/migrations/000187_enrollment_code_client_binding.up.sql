-- Migration 000187: enrollment codes carry the client and site binding of
-- their creator (WP-677, F-406; COL-04, TEN-09, E-08).
--
-- An enrollment code (000042) carried its organization only. Redeeming it
-- (POST /collectors/enroll, unauthenticated) registered the collector with
-- the client_id of the request body: a principal restricted to client 1
-- with collector:manage minted a code and enrolled an active collector with
-- a client certificate in client 2, or an org-wide one, although
-- POST /collectors refuses both to it (collector_isolation, 000058).
--
-- 1. binding_recorded marks codes whose binding is known; client_id and
--    site_id are the client and site the collector is enrolled in (NULL: the
--    creator held collector:manage org-wide in that dimension, the enrolling
--    collector may then name any client of the organization as before). The
--    server fills them when the code is minted: from the creator's scope, or
--    the client and site the creator chose within it. A site decides the
--    client (collector_enrollment_code_site_binding, like
--    collector_site_binding). Codes without a recorded binding are not
--    redeemed (fail-closed): the scope of their creator is not known.
-- 2. The restrictive policies collector_enrollment_code_scope_* apply the
--    client and site scope of the transaction to every command like the
--    policies of collector: a restricted principal writes only codes bound
--    to one of its clients and sites and reads those and the unbound codes
--    (which hold no more than a hash and a label). Redemption runs org-wide
--    (scope GUCs empty); the lookup by hash runs under the system flag
--    (000060, scope GUCs set to no access), which the SELECT policy admits
--    and no write policy does.
--
-- Codes minted before this migration keep binding_recorded false and are no
-- longer accepted; an administrator mints a new one (valid 24 h at most,
-- docs/betrieb/upgrade-hinweise.md).

ALTER TABLE collector_enrollment_code
    ADD COLUMN binding_recorded BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN client_id UUID,
    ADD COLUMN site_id UUID,
    ADD CONSTRAINT collector_enrollment_code_client_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client(id, organization_id) ON DELETE CASCADE,
    ADD CONSTRAINT collector_enrollment_code_site_fkey FOREIGN KEY (site_id, organization_id)
        REFERENCES site(id, organization_id) ON DELETE CASCADE,
    ADD CONSTRAINT collector_enrollment_code_binding_check CHECK (
        binding_recorded OR (client_id IS NULL AND site_id IS NULL));

-- The site of a code decides its client; a client that differs from the
-- site's is refused.
CREATE FUNCTION collector_enrollment_code_site_binding() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    site_client UUID;
BEGIN
    IF NEW.site_id IS NOT NULL THEN
        SELECT client_id INTO site_client FROM site WHERE id = NEW.site_id AND organization_id = NEW.organization_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'site not found' USING ERRCODE = 'foreign_key_violation',
                CONSTRAINT = 'collector_enrollment_code_site_fkey';
        END IF;
        IF NEW.client_id IS NOT NULL AND NEW.client_id <> site_client THEN
            RAISE EXCEPTION 'the site belongs to another client' USING ERRCODE = 'check_violation',
                CONSTRAINT = 'collector_enrollment_code_site_client';
        END IF;
        NEW.client_id := site_client;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_collector_enrollment_code_site_binding
    BEFORE INSERT OR UPDATE OF site_id, client_id ON collector_enrollment_code
    FOR EACH ROW EXECUTE FUNCTION collector_enrollment_code_site_binding();

DO $$
DECLARE
    within CONSTANT text :=
        '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
        'OR client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])) '
        'AND (NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL '
        'OR site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]))';
    -- Reading also shows the unbound codes of the organization, as
    -- collector_isolation shows org-wide collectors (E-11).
    readable CONSTANT text :=
        '(NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL OR client_id IS NULL '
        'OR client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[])) '
        'AND (NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL OR site_id IS NULL '
        'OR site_id = ANY (string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]))';
BEGIN
    EXECUTE format('CREATE POLICY collector_enrollment_code_scope_select ON collector_enrollment_code '
                   'AS RESTRICTIVE FOR SELECT USING (current_setting(''app.system'', true) = ''on'' OR (%s))', readable);
    EXECUTE format('CREATE POLICY collector_enrollment_code_scope_insert ON collector_enrollment_code '
                   'AS RESTRICTIVE FOR INSERT WITH CHECK (%s)', within);
    EXECUTE format('CREATE POLICY collector_enrollment_code_scope_update ON collector_enrollment_code '
                   'AS RESTRICTIVE FOR UPDATE USING (%1$s) WITH CHECK (%1$s)', within);
    EXECUTE format('CREATE POLICY collector_enrollment_code_scope_delete ON collector_enrollment_code '
                   'AS RESTRICTIVE FOR DELETE USING (%s)', within);
END
$$;
