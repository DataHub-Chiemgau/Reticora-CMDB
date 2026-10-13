-- Migration 000122: notification delivery, in-app inbox and preferences
-- (NTF-04, WP-139).
--
-- notification_delivery holds one message to one recipient over one
-- channel (a webhook channel: one row for all recipients) with its status
-- and retry schedule; a dedup key makes a repeated notification of the
-- same fact a no-op. notification_inbox is the in-app notification center
-- (UI-16). notification_preference holds a user's quiet hours and muted
-- categories; critical notifications ignore both.

CREATE TABLE notification_delivery (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    channel_id        UUID NOT NULL REFERENCES notification_channel(id) ON DELETE CASCADE,
    recipient_user_id UUID REFERENCES app_user(id) ON DELETE CASCADE,
    recipient_email   TEXT,
    recipient_name    TEXT,
    recipient_key     TEXT NOT NULL,
    category          TEXT NOT NULL,
    critical          BOOLEAN NOT NULL DEFAULT false,
    subject           TEXT NOT NULL,
    body              TEXT NOT NULL DEFAULT '',
    data              JSONB NOT NULL DEFAULT '{}',
    recipients        JSONB NOT NULL DEFAULT '[]',
    dedup_key         TEXT,
    status            TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'suppressed')),
    attempts          INTEGER NOT NULL DEFAULT 0,
    max_attempts      INTEGER NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error        TEXT,
    provider_id       TEXT,
    sent_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_notification_delivery_dedup
    ON notification_delivery (organization_id, channel_id, recipient_key, dedup_key) WHERE dedup_key IS NOT NULL;
CREATE INDEX idx_notification_delivery_due
    ON notification_delivery (next_attempt_at) WHERE status = 'pending';
CREATE INDEX idx_notification_delivery_org ON notification_delivery (organization_id, created_at DESC);

CREATE TRIGGER trg_notification_delivery_updated_at BEFORE UPDATE ON notification_delivery
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE notification_inbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    category        TEXT NOT NULL,
    subject         TEXT NOT NULL,
    body            TEXT NOT NULL DEFAULT '',
    data            JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at         TIMESTAMPTZ
);

CREATE INDEX idx_notification_inbox_user ON notification_inbox (organization_id, user_id, created_at DESC);
CREATE INDEX idx_notification_inbox_unread ON notification_inbox (organization_id, user_id) WHERE read_at IS NULL;

CREATE TABLE notification_preference (
    organization_id  UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    user_id          UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    quiet_start      TIME,
    quiet_end        TIME,
    time_zone        TEXT NOT NULL DEFAULT 'Europe/Berlin',
    muted_categories TEXT[] NOT NULL DEFAULT '{}',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id),
    CONSTRAINT notification_preference_quiet_check CHECK ((quiet_start IS NULL) = (quiet_end IS NULL))
);

CREATE TRIGGER trg_notification_preference_updated_at BEFORE UPDATE ON notification_preference
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE notification_delivery ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_delivery FORCE ROW LEVEL SECURITY;
CREATE POLICY notification_delivery_isolation ON notification_delivery FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE POLICY notification_delivery_system_select ON notification_delivery FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');

ALTER TABLE notification_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_inbox FORCE ROW LEVEL SECURITY;
CREATE POLICY notification_inbox_isolation ON notification_inbox FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);

ALTER TABLE notification_preference ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_preference FORCE ROW LEVEL SECURITY;
CREATE POLICY notification_preference_isolation ON notification_preference FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
