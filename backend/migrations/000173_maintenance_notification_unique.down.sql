-- Migration maintenance_notification_unique down: drops the unique index.
-- The duplicates removed by up are not restored.

DROP INDEX IF EXISTS maintenance_notification_window_client_channel_uniq;
