-- Migration 000289: origin of the entitlement rows and the date of the last
-- plan change (WP-880, PKG-01; E-155 option 1).
--
-- 1. entitlement.origin says where a row comes from: plan (a feature of the
--    organization's plan), addon (an add-on of CH14: iga, ai, admp) or
--    manual (a single grant of the operator). A plan change (WP-882)
--    replaces only the rows of origin plan; add-ons and manual grants stay.
--    CHECK entitlement_origin_check.
-- 2. Backfill per row: an add-on key is addon; a key of the plan matrix of
--    organization.plan is plan; every other row is manual. The matrix below
--    is planFeatures of internal/entitlement after WP-862 (E-151);
--    TestEntitlementOriginBackfill compares it with PlanFeatures.
-- 3. organization.plan_changed_at is the time of the last plan change
--    (WP-882); NULL until then. organization.plan stays the leading value.
-- No table, RLS or policy changes.

ALTER TABLE entitlement ADD COLUMN origin text NOT NULL DEFAULT 'manual'
    CONSTRAINT entitlement_origin_check CHECK (origin IN ('plan', 'addon', 'manual'));

COMMENT ON COLUMN entitlement.origin IS
    'Origin of the row: plan (feature of organization.plan), addon (add-on of CH14) or manual (single grant); WP-880.';

WITH plan_matrix(plan, feature_key) AS (VALUES
    ('essential', 'cmdb_core'),
    ('essential', 'discovery'),
    ('essential', 'topology'),
    ('essential', 'rack_view'),
    ('essential', 'export_csv'),
    ('essential', 'webhooks'),
    ('essential', 'api_access'),
    ('essential', 'notifications_email'),
    ('essential', 'assets_basic'),
    ('essential', 'documents'),
    ('essential', 'import'),
    ('essential', 'ticketing_essential'),
    ('standard', 'cmdb_core'),
    ('standard', 'discovery'),
    ('standard', 'topology'),
    ('standard', 'rack_view'),
    ('standard', 'export_csv'),
    ('standard', 'webhooks'),
    ('standard', 'api_access'),
    ('standard', 'notifications_email'),
    ('standard', 'assets_basic'),
    ('standard', 'documents'),
    ('standard', 'import'),
    ('standard', 'ticketing_essential'),
    ('standard', 'ticketing_standard'),
    ('standard', 'signature'),
    ('standard', 'stock'),
    ('standard', 'stocktake_standard'),
    ('standard', 'barcode'),
    ('standard', 'gps_standard'),
    ('standard', 'workplace'),
    ('standard', 'export_datev'),
    ('standard', 'monitoring'),
    ('standard', 'ipam'),
    ('pro', 'cmdb_core'),
    ('pro', 'discovery'),
    ('pro', 'topology'),
    ('pro', 'rack_view'),
    ('pro', 'export_csv'),
    ('pro', 'webhooks'),
    ('pro', 'api_access'),
    ('pro', 'notifications_email'),
    ('pro', 'assets_basic'),
    ('pro', 'documents'),
    ('pro', 'import'),
    ('pro', 'ticketing_essential'),
    ('pro', 'ticketing_standard'),
    ('pro', 'signature'),
    ('pro', 'stock'),
    ('pro', 'stocktake_standard'),
    ('pro', 'barcode'),
    ('pro', 'gps_standard'),
    ('pro', 'workplace'),
    ('pro', 'export_datev'),
    ('pro', 'monitoring'),
    ('pro', 'ipam'),
    ('pro', 'internal_orders'),
    ('pro', 'monitoring_network'),
    ('pro', 'credential_vault'),
    ('pro', 'notifications_sms_pager'),
    ('pro', 'workflows'),
    ('pro', 'compliance_findings'),
    ('enterprise', 'cmdb_core'),
    ('enterprise', 'discovery'),
    ('enterprise', 'topology'),
    ('enterprise', 'rack_view'),
    ('enterprise', 'export_csv'),
    ('enterprise', 'webhooks'),
    ('enterprise', 'api_access'),
    ('enterprise', 'notifications_email'),
    ('enterprise', 'assets_basic'),
    ('enterprise', 'documents'),
    ('enterprise', 'import'),
    ('enterprise', 'ticketing_essential'),
    ('enterprise', 'ticketing_standard'),
    ('enterprise', 'signature'),
    ('enterprise', 'stock'),
    ('enterprise', 'stocktake_standard'),
    ('enterprise', 'barcode'),
    ('enterprise', 'gps_standard'),
    ('enterprise', 'workplace'),
    ('enterprise', 'export_datev'),
    ('enterprise', 'monitoring'),
    ('enterprise', 'ipam'),
    ('enterprise', 'internal_orders'),
    ('enterprise', 'monitoring_network'),
    ('enterprise', 'credential_vault'),
    ('enterprise', 'notifications_sms_pager'),
    ('enterprise', 'workflows'),
    ('enterprise', 'compliance_findings'),
    ('enterprise', 'monitoring_access'),
    ('enterprise', 'monitoring_analytics'),
    ('enterprise', 'compliance_automation'),
    ('enterprise', 'endpoint_agent')
)
UPDATE entitlement e SET origin = CASE
        WHEN e.feature_key IN ('iga', 'ai', 'admp') THEN 'addon'
        WHEN EXISTS (SELECT 1 FROM organization o JOIN plan_matrix m ON m.plan = o.plan
                      WHERE o.id = e.organization_id AND m.feature_key = e.feature_key) THEN 'plan'
        ELSE 'manual'
    END;

ALTER TABLE organization ADD COLUMN plan_changed_at timestamptz;

COMMENT ON COLUMN organization.plan_changed_at IS
    'Time of the last plan change (WP-882); NULL while the plan of the creation applies.';
