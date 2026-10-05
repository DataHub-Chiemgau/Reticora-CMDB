-- Migration 000084 down: scalar limit_value and expires_at again.

ALTER TABLE entitlement
    ADD COLUMN limit_value BIGINT,
    ADD COLUMN expires_at TIMESTAMPTZ;

ALTER TABLE entitlement DROP CONSTRAINT entitlement_core_always_active;
UPDATE entitlement SET feature_key = 'cmdb' WHERE feature_key = 'cmdb_core';
UPDATE entitlement SET feature_key = 'export' WHERE feature_key = 'export_csv';

UPDATE entitlement SET
    limit_value = COALESCE(limits->>'max_cis', limits->>'limit')::bigint,
    expires_at = valid_until;

ALTER TABLE entitlement
    DROP CONSTRAINT entitlement_limits_object,
    DROP CONSTRAINT entitlement_source_check,
    DROP COLUMN source,
    DROP COLUMN valid_until,
    DROP COLUMN limits;
