-- Migration 000190: entitlement keys of phase 2 after ENT-06 (WP-244, E-44,
-- E-34).
--
-- The former module keys give way to the keys of ENT-06: inventory →
-- assets_basic, ticketing → ticketing_essential, stocktake →
-- stocktake_standard, with plan, enabled, limits, valid_until and source of
-- the former row (trial rows with source selfsignup included). An
-- organization of plan standard, pro or enterprise also gets stock as a copy
-- of inventory and ticketing_standard as a copy of ticketing; an essential
-- organization gets no stock. Every organization holding at least one
-- entitlement row then gets the phase-2 keys of its plan it still lacks
-- (enabled, no limits, source manual): all plans assets_basic, documents,
-- import and ticketing_essential; from standard on also ticketing_standard,
-- signature, stock, stocktake_standard, barcode, gps_standard, workplace and
-- export_datev; from pro on internal_orders. Phase-1 and P3/P4 rows stay as
-- they are; their missing plan rows, and every row of an organization
-- without rows, come from Provision at the next start (WP-470).
--
-- The former rows are kept in migration_quarantine (reason 'ENT-06 key
-- change (WP-244): former row'), every row added here is noted there as well
-- (reason 'ENT-06 key change (WP-244): added row'), so the down migration
-- restores the former rows exactly.

-- (0) Keep the former rows.
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT e.organization_id, 'entitlement', to_jsonb(e), 'ENT-06 key change (WP-244): former row', '000190'
FROM entitlement e
WHERE e.feature_key IN ('inventory', 'ticketing', 'stocktake');

-- (1) Successors with the fields of the former row.
WITH added AS (
    INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source)
    SELECT e.organization_id, k.successor, e.plan, e.enabled, e.limits, e.valid_until, e.source
    FROM organization o
    JOIN entitlement e ON e.organization_id = o.id
    JOIN (VALUES ('inventory', 'assets_basic'),
                 ('ticketing', 'ticketing_essential'),
                 ('stocktake', 'stocktake_standard')) AS k (former, successor) ON k.former = e.feature_key
    ON CONFLICT (organization_id, feature_key) DO NOTHING
    RETURNING *
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT added.organization_id, 'entitlement', to_jsonb(added), 'ENT-06 key change (WP-244): added row', '000190'
FROM added;

-- (2) Copies of the former row for organizations from standard on.
WITH added AS (
    INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source)
    SELECT e.organization_id, k.copy, e.plan, e.enabled, e.limits, e.valid_until, e.source
    FROM organization o
    JOIN entitlement e ON e.organization_id = o.id
    JOIN (VALUES ('inventory', 'stock'),
                 ('ticketing', 'ticketing_standard')) AS k (former, copy) ON k.former = e.feature_key
    WHERE o.plan IN ('standard', 'pro', 'enterprise')
    ON CONFLICT (organization_id, feature_key) DO NOTHING
    RETURNING *
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT added.organization_id, 'entitlement', to_jsonb(added), 'ENT-06 key change (WP-244): added row', '000190'
FROM added;

-- (3) The missing phase-2 keys of the plan (E-44) for every organization
-- with entitlement rows. Tier 1 is every plan, 2 from standard, 3 from pro.
WITH added AS (
    INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, source)
    SELECT o.id, k.feature_key, o.plan, true, '{}'::jsonb, 'manual'
    FROM organization o
    JOIN (VALUES ('assets_basic', 1), ('documents', 1), ('import', 1), ('ticketing_essential', 1),
                 ('ticketing_standard', 2), ('signature', 2), ('stock', 2), ('stocktake_standard', 2),
                 ('barcode', 2), ('gps_standard', 2), ('workplace', 2), ('export_datev', 2),
                 ('internal_orders', 3)) AS k (feature_key, tier)
        ON k.tier <= CASE o.plan WHEN 'standard' THEN 2 WHEN 'pro' THEN 3 WHEN 'enterprise' THEN 3 ELSE 1 END
    WHERE EXISTS (SELECT 1 FROM entitlement e WHERE e.organization_id = o.id)
    ON CONFLICT (organization_id, feature_key) DO NOTHING
    RETURNING *
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT added.organization_id, 'entitlement', to_jsonb(added), 'ENT-06 key change (WP-244): added row', '000190'
FROM added;

-- (4) The former rows go.
DELETE FROM entitlement WHERE feature_key IN ('inventory', 'ticketing', 'stocktake');
