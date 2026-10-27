-- Migration 000173: one notification per maintenance window, client and
-- channel (WP-540, MGT-04, NTF-05, NFR-04; F-235).
--
-- POST /maintenance-windows/{id}/notify inserted one pending row per
-- affected client on every call: announcing a window twice queued every
-- client twice, and the notification chain would notify the customers twice.
--
-- Existing duplicates are reduced to the oldest row of each window, client
-- and channel (created_at, then id); no channel delivers maintenance
-- notifications yet, so every row is pending and none carries a delivery.
-- The unique index then lets NotifyClients insert with ON CONFLICT DO
-- NOTHING. client_id is NULL for no row the repository writes; NULLs stay
-- distinct. The policy of maintenance_notification is unchanged.

WITH ranked AS (
    SELECT id, row_number() OVER (
        PARTITION BY maintenance_window_id, client_id, channel
        ORDER BY created_at, id) AS rn
    FROM maintenance_notification
    WHERE client_id IS NOT NULL
)
DELETE FROM maintenance_notification n
USING ranked
WHERE n.id = ranked.id AND ranked.rn > 1;

CREATE UNIQUE INDEX maintenance_notification_window_client_channel_uniq
    ON maintenance_notification (maintenance_window_id, client_id, channel);
