-- Migration 000263 down: the required context fields of 000055 return to
-- the global states in_stock, deployed and disposed of physical_asset.

UPDATE lifecycle_state
SET required_fields = CASE key
        WHEN 'in_stock' THEN '["storage_location"]'::jsonb
        WHEN 'deployed' THEN '["deployment_location"]'::jsonb
        WHEN 'disposed' THEN '["disposal_date", "disposal_record"]'::jsonb
    END
WHERE definition_id = '00000000-0000-0000-0000-000000000101'
  AND organization_id IS NULL
  AND key IN ('in_stock', 'deployed', 'disposed');
