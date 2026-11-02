-- Migration 000185: API keys bound to a service account carry the scope of
-- their creator (WP-668, F-397; RBA-08, AUT-04, TEN-09).
--
-- A bound key acts with the rights of its service account (000082). The
-- creator's scope was checked only at binding (F-007): a key a client-1
-- delegate bound to an account without roles, or with roles for client 1,
-- read every client once an org-wide administrator later gave the account
-- org-wide roles.
--
-- creator_scope snapshots, per key permission, the client, site and team
-- scope in which the creator held it at binding, like the scope snapshot of
-- webhook subscriptions (000155, E-98): an object of permission keys whose
-- values have the dimensions clients, sites and teams, each {"all": true} or
-- {"ids": [...]} (none: {}). Every request intersects the account's current
-- rights with it, so the key follows reductions of the account and survives
-- the creator's deactivation (E-64) but never grants more than its creator
-- held. A rotation copies it to the successor.
--
-- Keys bound before this migration keep NULL: their creator's scope at
-- binding is not known. They are limited to the current rights of their
-- creator instead (fail-closed, docs/betrieb/upgrade-hinweise.md). Keys not
-- bound to an account act with their owner's current rights and keep NULL.

ALTER TABLE api_key
    ADD COLUMN creator_scope JSONB,
    ADD CONSTRAINT api_key_creator_scope_check CHECK (
        creator_scope IS NULL OR (service_account_id IS NOT NULL AND jsonb_typeof(creator_scope) = 'object'));
