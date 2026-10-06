-- Migration 000084: entitlement model after ENT-01/ENT-02 (WP-071).
--
-- entitlement(org, feature_key unique per org, enabled, limits jsonb,
-- valid_until, source manual|selfsignup|billing|reseller). The scalar
-- limit_value becomes a named quota in limits (max_cis for the CMDB, limit
-- for any other feature), expires_at becomes valid_until, and the feature
-- keys follow ENT-02 (cmdb → cmdb_core, export → export_csv).

ALTER TABLE entitlement
    ADD COLUMN limits JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN valid_until TIMESTAMPTZ,
    ADD COLUMN source TEXT NOT NULL DEFAULT 'manual',
    ADD CONSTRAINT entitlement_source_check CHECK (source IN ('manual', 'selfsignup', 'billing', 'reseller')),
    ADD CONSTRAINT entitlement_limits_object CHECK (jsonb_typeof(limits) = 'object');

UPDATE entitlement SET
    limits = CASE
        WHEN limit_value IS NULL THEN '{}'::jsonb
        WHEN feature_key = 'cmdb' THEN jsonb_build_object('max_cis', limit_value)
        ELSE jsonb_build_object('limit', limit_value)
    END,
    valid_until = expires_at;

UPDATE entitlement SET feature_key = 'cmdb_core' WHERE feature_key = 'cmdb';
UPDATE entitlement SET feature_key = 'export_csv' WHERE feature_key = 'export';

-- cmdb_core is always active and never expires (ENT-02).
UPDATE entitlement SET enabled = true, valid_until = NULL WHERE feature_key = 'cmdb_core';
ALTER TABLE entitlement ADD CONSTRAINT entitlement_core_always_active
    CHECK (feature_key <> 'cmdb_core' OR (enabled AND valid_until IS NULL));

ALTER TABLE entitlement DROP COLUMN limit_value, DROP COLUMN expires_at;
