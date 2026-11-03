-- Migration 000216: business calendars per organization and client
-- (TKT-02, LOC-01/02, WP-292).
--
-- 1. business_calendar holds the working hours of a calendar as a list of
--    windows in local wall time (weekly_hours: [{weekday 1=Mon…7=Sun,
--    start "HH:MM", end "HH:MM"}]) and its holidays ([{date "YYYY-MM-DD",
--    name}]); the application checks both (sla.Calendar.Validate). timezone
--    is optional: without it the SLA clock uses client.timezone of the
--    ticket's client, otherwise organization.timezone. client_id NULL is an
--    org-wide calendar. At most one default calendar per organization and
--    client: a partial unique index with NULLS NOT DISTINCT (no expression
--    index, K22).
-- 2. The client reference carries the organization (K19, pattern 000136).
-- 3. Row level security with ENABLE and FORCE: USING shows org-wide
--    calendars to a client-scoped principal, WITH CHECK only lets it write
--    calendars of its clients (E-09); the restrictive UPDATE and DELETE
--    policies keep it from changing org-wide rows (pattern 000129 with the
--    client predicate only). Truncate guard of WP-352.
-- 4. sla.calendar_id names the calendar of a policy; ticket_sla.calendar_id
--    keeps the calendar the due times were computed with (NULL: elapsed
--    time or the built-in standard). Deleting a calendar sets both to NULL;
--    computed due times stay. ticket_sla.sla_explicit records a policy the
--    caller chose; the two *_breach_notified_at columns and their partial
--    indexes serve the breach run of WP-769.

CREATE TABLE business_calendar (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID,
    name            TEXT NOT NULL,
    timezone        TEXT CONSTRAINT business_calendar_timezone_check
                        CHECK (timezone IS NULL OR reticora_is_iana_timezone(timezone)),
    weekly_hours    JSONB NOT NULL DEFAULT '[{"weekday": 1, "start": "08:00", "end": "18:00"}, {"weekday": 2, "start": "08:00", "end": "18:00"}, {"weekday": 3, "start": "08:00", "end": "18:00"}, {"weekday": 4, "start": "08:00", "end": "18:00"}, {"weekday": 5, "start": "08:00", "end": "18:00"}]'
                        CONSTRAINT business_calendar_weekly_hours_check CHECK (jsonb_typeof(weekly_hours) = 'array'),
    holidays        JSONB NOT NULL DEFAULT '[]'
                        CONSTRAINT business_calendar_holidays_check CHECK (jsonb_typeof(holidays) = 'array'),
    is_default      BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT business_calendar_id_organization_key UNIQUE (id, organization_id),
    CONSTRAINT business_calendar_client_id_fkey FOREIGN KEY (client_id, organization_id)
        REFERENCES client (id, organization_id) ON DELETE CASCADE
);
CREATE INDEX idx_business_calendar_client ON business_calendar (organization_id, client_id);
CREATE UNIQUE INDEX business_calendar_default ON business_calendar (organization_id, client_id) NULLS NOT DISTINCT
    WHERE is_default;
CREATE TRIGGER trg_business_calendar_updated_at BEFORE UPDATE ON business_calendar
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE business_calendar ENABLE ROW LEVEL SECURITY;
ALTER TABLE business_calendar FORCE ROW LEVEL SECURITY;
CREATE POLICY business_calendar_isolation ON business_calendar FOR ALL
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY business_calendar_scoped_update ON business_calendar AS RESTRICTIVE FOR UPDATE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])))
    WITH CHECK (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE POLICY business_calendar_scoped_delete ON business_calendar AS RESTRICTIVE FOR DELETE
    USING (organization_id = (current_setting('app.org_id'))::uuid
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])));
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON business_calendar
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── Calendar of a policy and of a ticket's SLA state ─────────────────────
ALTER TABLE sla ADD COLUMN calendar_id UUID,
    ADD CONSTRAINT sla_calendar_id_fkey FOREIGN KEY (calendar_id, organization_id)
        REFERENCES business_calendar (id, organization_id) ON DELETE SET NULL (calendar_id);
CREATE INDEX idx_sla_calendar ON sla (organization_id, calendar_id);

ALTER TABLE ticket_sla ADD COLUMN calendar_id UUID,
    ADD COLUMN sla_explicit BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN response_breach_notified_at TIMESTAMPTZ,
    ADD COLUMN resolution_breach_notified_at TIMESTAMPTZ,
    ADD CONSTRAINT ticket_sla_calendar_id_fkey FOREIGN KEY (calendar_id, organization_id)
        REFERENCES business_calendar (id, organization_id) ON DELETE SET NULL (calendar_id);
CREATE INDEX idx_ticket_sla_calendar ON ticket_sla (organization_id, calendar_id);
CREATE INDEX idx_ticket_sla_response_open ON ticket_sla (organization_id, response_due_at)
    WHERE first_response_at IS NULL AND response_breach_notified_at IS NULL;
CREATE INDEX idx_ticket_sla_resolution_open ON ticket_sla (organization_id, resolution_due_at)
    WHERE resolved_at IS NULL AND resolution_breach_notified_at IS NULL;
