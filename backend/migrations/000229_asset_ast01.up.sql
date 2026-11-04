-- WP-271 (ast-core, AST-01, DB-05): the asset core after AST-01.
--
-- * New columns manufacturer, model, cost_center, supplier_id, contract_id,
--   owner_user_id and owner_team_id. The references carry the organization
--   (K19); they are only the fallback, the repository checks scope and
--   client before it writes. owner_user_id has no delete action (E-124): a
--   user who owns an asset cannot be deleted (ErrUserReferenced, 409);
--   deleting a team clears owner_team_id only.
-- * barcode is unique per organization. Duplicates of the existing rows
--   keep the value on the oldest row (created_at, then id); the others are
--   copied to migration_quarantine (reason duplicate_barcode) and cleared.
-- * currency: the column default EUR is gone. The trigger
--   asset_default_currency sets organization.currency when currency is NULL
--   or empty, on every insert and every update of currency; existing rows
--   without a currency get the currency of their organization.
-- * Suppliers (E-41): each distinct, non-empty btrim(supplier) of an
--   organization becomes an org-wide supplier row (an existing row of the
--   same name is reused) and fills supplier_id. The free text supplier stays
--   as deprecated until WP-738.
-- * lifecycle_state (E-120 (a)) is backfilled from status: in_stock →
--   in_stock, assigned → deployed, maintenance → repair, retired → retired,
--   disposed → disposed, if the state belongs to the effective asset model
--   (lifecycle_model_effective, physical_asset without one). Rows left
--   without a state (lost and unknown statuses) are kept and copied to
--   migration_quarantine.
-- * location_id: rows with custom_fields.location_id (written by the
--   inventory conversion before WP-271) and no location_id take the value
--   when it is the UUID of a location of the same organization; the
--   triggers derive site_id and propagate the location to linked CIs.

-- 1. Columns and references.
ALTER TABLE asset
    ADD COLUMN manufacturer TEXT,
    ADD COLUMN model TEXT,
    ADD COLUMN cost_center TEXT,
    ADD COLUMN supplier_id UUID,
    ADD COLUMN contract_id UUID,
    ADD COLUMN owner_user_id UUID,
    ADD COLUMN owner_team_id UUID;

ALTER TABLE asset
    ADD CONSTRAINT asset_supplier_id_fkey FOREIGN KEY (supplier_id, organization_id)
        REFERENCES supplier (id, organization_id),
    ADD CONSTRAINT asset_contract_id_fkey FOREIGN KEY (contract_id, organization_id)
        REFERENCES contract (id, organization_id),
    ADD CONSTRAINT asset_owner_user_id_fkey FOREIGN KEY (owner_user_id, organization_id)
        REFERENCES app_user (id, organization_id),
    ADD CONSTRAINT asset_owner_team_id_fkey FOREIGN KEY (owner_team_id, organization_id)
        REFERENCES team (id, organization_id) ON DELETE SET NULL (owner_team_id);

CREATE INDEX IF NOT EXISTS idx_asset_supplier ON asset (supplier_id, organization_id) WHERE supplier_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_asset_contract ON asset (contract_id, organization_id) WHERE contract_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_asset_owner_user ON asset (owner_user_id, organization_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_asset_owner_team ON asset (owner_team_id, organization_id) WHERE owner_team_id IS NOT NULL;

-- 2. Barcode unique per organization.
WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY organization_id, barcode ORDER BY created_at, id) AS rn
    FROM asset
    WHERE barcode IS NOT NULL
), duplicate AS (
    SELECT a.* FROM asset a JOIN ranked r ON r.id = a.id WHERE r.rn > 1
), quarantined AS (
    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
    SELECT organization_id, 'asset', to_jsonb(duplicate), 'duplicate_barcode', '000229'
    FROM duplicate
    RETURNING 1
)
UPDATE asset SET barcode = NULL WHERE id IN (SELECT id FROM duplicate);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_barcode_org ON asset (organization_id, barcode) WHERE barcode IS NOT NULL;

-- 3. Currency of the organization instead of the fixed default EUR.
ALTER TABLE asset ALTER COLUMN currency DROP DEFAULT;

CREATE FUNCTION asset_default_currency() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.currency IS NULL OR btrim(NEW.currency) = '' THEN
        SELECT o.currency INTO NEW.currency FROM organization o WHERE o.id = NEW.organization_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER asset_default_currency
    BEFORE INSERT OR UPDATE OF currency ON asset
    FOR EACH ROW EXECUTE FUNCTION asset_default_currency();

UPDATE asset a SET currency = o.currency
FROM organization o
WHERE o.id = a.organization_id AND (a.currency IS NULL OR btrim(a.currency) = '');

-- 4. Suppliers from the free text (E-41).
INSERT INTO supplier (organization_id, name)
SELECT DISTINCT organization_id, btrim(supplier)
FROM asset
WHERE supplier IS NOT NULL AND btrim(supplier) <> ''
ON CONFLICT (organization_id, name) DO NOTHING;

UPDATE asset a SET supplier_id = s.id
FROM supplier s
WHERE s.organization_id = a.organization_id AND a.supplier IS NOT NULL AND btrim(a.supplier) <> ''
  AND s.name = btrim(a.supplier);

-- 5. lifecycle_state from status (E-120 (a)).
WITH mapped AS (
    SELECT a.id,
           CASE a.status
               WHEN 'in_stock' THEN 'in_stock'
               WHEN 'assigned' THEN 'deployed'
               WHEN 'maintenance' THEN 'repair'
               WHEN 'retired' THEN 'retired'
               WHEN 'disposed' THEN 'disposed'
           END AS state,
           COALESCE(lifecycle_model_effective(a.asset_type_id, 'asset'),
                    '00000000-0000-0000-0000-000000000101'::uuid) AS model
    FROM asset a
    WHERE a.lifecycle_state IS NULL
)
UPDATE asset a SET lifecycle_state = m.state
FROM mapped m
WHERE a.id = m.id AND m.state IS NOT NULL
  AND EXISTS (SELECT 1 FROM lifecycle_state s WHERE s.definition_id = m.model AND s.key = m.state);

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT a.organization_id, 'asset', to_jsonb(a),
       'asset without lifecycle_state: status ' || a.status || ' has no state of the asset lifecycle model (E-120)',
       '000229'
FROM asset a
WHERE a.lifecycle_state IS NULL;

-- 6. Location from custom_fields.location_id (inventory conversion).
UPDATE asset a SET location_id = l.id
FROM location l
WHERE a.location_id IS NULL
  AND a.custom_fields ? 'location_id'
  AND l.organization_id = a.organization_id
  AND l.id = CASE
                 WHEN (a.custom_fields ->> 'location_id') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                 THEN (a.custom_fields ->> 'location_id')::uuid
             END;
