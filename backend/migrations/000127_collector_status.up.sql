-- Migration 000127: collector status model and site binding (COL-01,
-- COL-05, WP-143).
--
-- A collector is pending (created, not yet connected), active (sends
-- heartbeats), offline (no heartbeat for five minutes, set by the
-- collector.status run of the scheduler), disabled (by an administrator)
-- or paused (the organization's discovery license is not active, ENT-07).
-- Former statuses: online and degraded become active, offline stays.
-- A collector belongs to a site of its client; the site decides the client.
-- cert_fingerprint and cert_not_after describe the collector certificate
-- (filled by the enrollment PKI, WP-145).

ALTER TABLE collector DROP CONSTRAINT collector_status_check;
UPDATE collector SET status = CASE status WHEN 'offline' THEN 'offline' ELSE 'active' END;
ALTER TABLE collector ALTER COLUMN status SET DEFAULT 'pending';
ALTER TABLE collector ADD CONSTRAINT collector_status_check
    CHECK (status IN ('pending', 'active', 'offline', 'disabled', 'paused'));
ALTER TABLE collector RENAME COLUMN last_heartbeat TO last_heartbeat_at;
ALTER TABLE collector
    ADD COLUMN site_id           UUID REFERENCES site (id) ON DELETE SET NULL,
    ADD COLUMN cert_fingerprint  TEXT,
    ADD COLUMN cert_not_after    TIMESTAMPTZ,
    ADD COLUMN status_changed_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX idx_collector_heartbeat ON collector (organization_id, status, last_heartbeat_at);

-- The site belongs to the collector's organization and decides its client.
CREATE FUNCTION collector_site_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    site_org UUID;
    site_client UUID;
BEGIN
    IF NEW.site_id IS NOT NULL THEN
        SELECT organization_id, client_id INTO site_org, site_client FROM site WHERE id = NEW.site_id;
        IF site_org IS DISTINCT FROM NEW.organization_id THEN
            RAISE EXCEPTION 'site of another organization' USING ERRCODE = 'foreign_key_violation',
                CONSTRAINT = 'collector_site_id_fkey';
        END IF;
        IF NEW.client_id IS NOT NULL AND NEW.client_id <> site_client THEN
            RAISE EXCEPTION 'the site belongs to another client' USING ERRCODE = 'check_violation',
                CONSTRAINT = 'collector_site_client';
        END IF;
        NEW.client_id := site_client;
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.status IS DISTINCT FROM OLD.status THEN
        NEW.status_changed_at := now();
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_collector_site_binding BEFORE INSERT OR UPDATE ON collector
    FOR EACH ROW EXECUTE FUNCTION collector_site_binding();

-- The site scope of a principal applies to collectors as to the other
-- site-bound tables (TEN-06).
DROP POLICY collector_isolation ON collector;
DROP POLICY collector_scoped_update ON collector;
DROP POLICY collector_scoped_delete ON collector;
CREATE POLICY collector_isolation ON collector FOR ALL
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY collector_scoped_update ON collector AS RESTRICTIVE FOR UPDATE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
CREATE POLICY collector_scoped_delete ON collector AS RESTRICTIVE FOR DELETE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[])));
