DROP TABLE IF EXISTS maintenance_notification;
DROP TABLE IF EXISTS maintenance_window_ci;
DROP TABLE IF EXISTS maintenance_window;
DELETE FROM permission WHERE key IN ('maintenance:read','maintenance:write');
