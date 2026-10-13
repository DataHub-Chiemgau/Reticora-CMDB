-- Migration 000123: webhook secret rotation with overlap (SEC-06, WP-141).
--
-- A rotated webhook secret keeps the previous one valid for 24 hours: the
-- dispatcher signs with both, so a receiver can switch without losing
-- deliveries. The rotation job clears expired previous secrets.

ALTER TABLE webhook_subscription ADD COLUMN previous_secret TEXT;
ALTER TABLE webhook_subscription ADD COLUMN previous_secret_expires_at TIMESTAMPTZ;
ALTER TABLE webhook_subscription ADD CONSTRAINT webhook_subscription_previous_secret_check
    CHECK ((previous_secret IS NULL) = (previous_secret_expires_at IS NULL));
