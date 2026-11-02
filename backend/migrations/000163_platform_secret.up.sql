-- Migration 000163: platform_secret, the secrets of the installation
-- (SEC-06, WP-459, F-158).
--
-- The row derivation_root holds the derivation root: 32 random bytes the
-- server draws at its first start after the update, sealed with the master
-- key like a data key (AES-256-GCM, marked with the key id). The secrets the
-- server derives (the issuer and the logins of the collector NATS endpoint,
-- the key of the collector CA announcements, the pagination cursor key) come
-- from the root instead of the master key, so a master-key rotation leaves
-- them unchanged; rotation.master_key reseals the root with the new key
-- (rewrapped_at).
--
-- The table belongs to the installation and has no organization_id. Every
-- policy requires the system flag (app.system): the server reads the root
-- with database.WithSystem and creates and reseals it with
-- database.WithSystemWrite; tenant and operator transactions neither read
-- nor write it (rls.SystemTables, rule system-only). No policy allows
-- DELETE and reticora_app has no DELETE: without its root the server would
-- draw a new one and every derived secret would change. TRUNCATE ignores
-- row level security, so the table carries the TRUNCATE guard of the tenant
-- tables (migration 000144) against reticora_app and reticora_owner.

CREATE TABLE platform_secret (
    name         TEXT PRIMARY KEY,
    sealed       BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    rewrapped_at TIMESTAMPTZ
);

ALTER TABLE platform_secret ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_secret FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_secret_system_select ON platform_secret FOR SELECT
    USING (current_setting('app.system', true) = 'on');
CREATE POLICY platform_secret_system_insert ON platform_secret FOR INSERT
    WITH CHECK (current_setting('app.system', true) = 'on');
CREATE POLICY platform_secret_system_update ON platform_secret FOR UPDATE
    USING (current_setting('app.system', true) = 'on')
    WITH CHECK (current_setting('app.system', true) = 'on');

REVOKE DELETE ON platform_secret FROM reticora_app;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON platform_secret
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
