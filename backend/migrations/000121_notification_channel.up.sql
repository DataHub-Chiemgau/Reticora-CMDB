-- Migration 000121: notification channels (NTF-01, TEC-13, WP-137).
--
-- A channel names how an organization is notified: e-mail over the
-- central SMTP of the installation or the organization's own SMTP, in-app,
-- or a webhook; sms and pager follow with their providers (NTF-02, P3).
-- The provider configuration (SMTP credentials, webhook secret) is stored
-- encrypted: config_ciphertext is AES-GCM under a data key of its own,
-- config_dek that key wrapped with the master key (platform/crypto).

CREATE TABLE notification_channel (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('email', 'inapp', 'webhook', 'sms', 'pager')),
    provider          TEXT NOT NULL,
    config_ciphertext BYTEA,
    config_dek        BYTEA,
    key_version       INTEGER NOT NULL DEFAULT 1,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT notification_channel_name_unique UNIQUE (organization_id, name),
    CONSTRAINT notification_channel_provider_check CHECK (
        (kind = 'email' AND provider IN ('central_smtp', 'smtp'))
        OR (kind = 'inapp' AND provider = 'inapp')
        OR (kind = 'webhook' AND provider = 'webhook')),
    CONSTRAINT notification_channel_config_check CHECK ((config_ciphertext IS NULL) = (config_dek IS NULL))
);

CREATE INDEX idx_notification_channel_org ON notification_channel (organization_id, kind) WHERE enabled;

CREATE TRIGGER trg_notification_channel_updated_at BEFORE UPDATE ON notification_channel
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE notification_channel ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_channel FORCE ROW LEVEL SECURITY;
CREATE POLICY notification_channel_isolation ON notification_channel FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
