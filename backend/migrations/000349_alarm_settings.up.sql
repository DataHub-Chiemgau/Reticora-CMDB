-- WP-1030 (ALR-02, ALR-04, ALR-06, ALR-12, E-181, E-198, E-200): the alarm
-- settings of an organization, one row per organization; without a row the
-- defaults of alarm.Defaults apply.
--
-- settle_seconds        correlation window before the root notification
--                       (0..300 s, default 30)
-- flap_transitions      flapping: N state changes ... (2..100, default 5)
-- flap_window_seconds   ... within the window W (60..86400 s, default 900)
-- flap_hold_seconds     stable phase H that ends flapping (60..86400 s,
--                       default 900)
-- notify_rate_per_min   alarm notifications per minute before a summary
--                       (1..10000, default 60)
-- unknown_mode          how unknown counts in availability and SLA:
--                       excluded, down or up (default excluded, E-181)
-- notify_after_maintenance  notify still open alarms after a maintenance
--                       window (default true)
-- updated_by            the acting user, an actor id without foreign key
--                       (Konvention 16)
--
-- An organization setting: no client or site; RLS and FORCE RLS on
-- app.org_id (USING and WITH CHECK), the truncate guard (000144). The
-- handler lets only organization-wide principals write.

CREATE TABLE alarm_settings (
    organization_id uuid PRIMARY KEY REFERENCES organization (id) ON DELETE CASCADE,
    settle_seconds integer NOT NULL DEFAULT 30
        CONSTRAINT alarm_settings_settle_seconds_check CHECK (settle_seconds BETWEEN 0 AND 300),
    flap_transitions integer NOT NULL DEFAULT 5
        CONSTRAINT alarm_settings_flap_transitions_check CHECK (flap_transitions BETWEEN 2 AND 100),
    flap_window_seconds integer NOT NULL DEFAULT 900
        CONSTRAINT alarm_settings_flap_window_seconds_check CHECK (flap_window_seconds BETWEEN 60 AND 86400),
    flap_hold_seconds integer NOT NULL DEFAULT 900
        CONSTRAINT alarm_settings_flap_hold_seconds_check CHECK (flap_hold_seconds BETWEEN 60 AND 86400),
    notify_rate_per_min integer NOT NULL DEFAULT 60
        CONSTRAINT alarm_settings_notify_rate_per_min_check CHECK (notify_rate_per_min BETWEEN 1 AND 10000),
    unknown_mode text NOT NULL DEFAULT 'excluded'
        CONSTRAINT alarm_settings_unknown_mode_check CHECK (unknown_mode IN ('excluded', 'down', 'up')),
    notify_after_maintenance boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid
);

ALTER TABLE alarm_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE alarm_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY alarm_settings_isolation ON alarm_settings FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);

CREATE TRIGGER trg_alarm_settings_updated_at BEFORE UPDATE ON alarm_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON alarm_settings
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
