-- Migration notification_digest: digests of the non-critical notification
-- categories (NTF-04, WP-508, F-156).
--
-- notification_preference.digest maps a category to its window (hourly or
-- daily); the default bundles review notifications hourly. For a category
-- in the digest the notifier stores each e-mail as a row of
-- notification_digest_item instead of a delivery; the scheduler kind
-- notify.digest turns the items of an ended window into one
-- notification_delivery per channel, recipient, category and window
-- (delivery_id). Critical notifications, in-app and webhook channels are
-- never bundled. The dedup key of an item is that of the delivery it
-- replaces: a repeated notification of the same fact is a no-op.
--
-- Policies: notification_digest_item_isolation (ALL) rows of app.org_id
-- only; notification_digest_item_system_select (SELECT) lets the system flag
-- find the organizations with ended windows (as notification_delivery); the
-- run bundles them per organization in a tenant transaction.

ALTER TABLE notification_preference
    ADD COLUMN digest JSONB NOT NULL DEFAULT '{"review": "hourly"}'
        CONSTRAINT notification_preference_digest_check CHECK (jsonb_typeof(digest) = 'object');

CREATE TABLE notification_digest_item (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id   UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    channel_id        UUID NOT NULL REFERENCES notification_channel (id) ON DELETE CASCADE,
    recipient_user_id UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    recipient_email   TEXT NOT NULL,
    recipient_name    TEXT,
    recipient_key     TEXT NOT NULL,
    category          TEXT NOT NULL,
    digest_window     TEXT NOT NULL CHECK (digest_window IN ('hourly', 'daily')),
    window_end        TIMESTAMPTZ NOT NULL,
    subject           TEXT NOT NULL,
    body              TEXT NOT NULL DEFAULT '',
    data              JSONB NOT NULL DEFAULT '{}',
    dedup_key         TEXT,
    delivery_id       UUID REFERENCES notification_delivery (id) ON DELETE CASCADE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_notification_digest_item_dedup
    ON notification_digest_item (organization_id, channel_id, recipient_key, dedup_key) WHERE dedup_key IS NOT NULL;
CREATE INDEX idx_notification_digest_item_due
    ON notification_digest_item (organization_id, window_end) WHERE delivery_id IS NULL;
CREATE INDEX idx_notification_digest_item_delivery ON notification_digest_item (delivery_id);

ALTER TABLE notification_digest_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_digest_item FORCE ROW LEVEL SECURITY;
CREATE POLICY notification_digest_item_isolation ON notification_digest_item FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE POLICY notification_digest_item_system_select ON notification_digest_item FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');

-- Every tenant table carries the TRUNCATE guard of the tenant tables.
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON notification_digest_item
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
