-- Migration 000282: a default client for every organization (UI-15, TEN-01;
-- WP-854).
--
-- The organization is the tenant (MSP), client the customer within it. A
-- site needs a client (LOC-03), so an organization without one cannot use
-- sites, racks, the floor plan, the map or the client choice of the scope
-- switcher. Signup, the operator creation and the installer organization
-- "Reticora Demo" (000054, install-cloud.sh) created none.
--
-- 1. organization_default_client: every new organization gets a client named
--    like the organization with the slug default (pattern
--    organization_default_tags, 000218). client has FORCE RLS: the insert
--    runs in the new organization's tenant context with a widened client and
--    site scope (app_scope_widen, 000139), the caller's settings are
--    restored. ON CONFLICT DO NOTHING keeps the trigger harmless should the
--    organization already have such a client.
-- 2. Backfill (section "Backfill", executed by
--    backend/internal/signup/default_client_integration_test.go): every
--    organization without a client gets the default client,
--    00000000-0000-0000-0000-000000000001 ("Reticora Demo") included.
--    Organizations with clients get no further one. Existing org-wide objects
--    stay org-wide.

-- ─── 1. Trigger ────────────────────────────────────────────────────────────────

CREATE FUNCTION organization_default_client() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text := current_setting('app.org_id', true);
    previous_scope text[] := app_scope_widen();
BEGIN
    PERFORM set_config('app.org_id', NEW.id::text, true);
    INSERT INTO client (organization_id, name, slug)
    VALUES (NEW.id, NEW.name, 'default')
    ON CONFLICT DO NOTHING;
    PERFORM set_config('app.org_id', COALESCE(previous, ''), true);
    PERFORM app_scope_restore(previous_scope);
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_organization_default_client AFTER INSERT ON organization
    FOR EACH ROW EXECUTE FUNCTION organization_default_client();

-- ─── 2. Backfill ───────────────────────────────────────────────────────────────

INSERT INTO client (organization_id, name, slug)
SELECT o.id, o.name, 'default' FROM organization o
WHERE NOT EXISTS (SELECT 1 FROM client c WHERE c.organization_id = o.id)
ON CONFLICT DO NOTHING;

-- ─── end of backfill ───────────────────────────────────────────────────────────
