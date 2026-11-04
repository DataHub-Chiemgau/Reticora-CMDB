-- WP-743 (AST-05; E-26, K19): the legacy composition model goes. Since
-- WP-742 (000248) the child contract lives on ci.parent_ci_id and
-- ci.child_mode, no path writes composition any more, and WP-841 removed
-- the last reader. The unused column asset.parent_asset_id (000055) goes
-- with it.
--
-- 1. Every composition row, role and position included, is saved in
--    migration_quarantine (source_table 'composition', migration '000253',
--    row_data = the whole row), every set asset.parent_asset_id as
--    {id, parent_asset_id} (source_table 'asset'). The down migration
--    restores them from there.
-- 2. DROP TABLE composition takes its policy composition_isolation, the
--    triggers trg_composition_updated_at, trg_composition_no_cycle and
--    reticora_truncate_guard, the indexes and the foreign keys (000055,
--    000056, 000057, 000144) with it; the cycle function
--    reject_composition_cycle (000057) is dropped after it.
-- 3. DROP COLUMN asset.parent_asset_id takes asset_parent_asset_id_fkey.
--
-- A repair of a saved row goes through PATCH /api/v1/cis/{id} with
-- parent_ci_id and child_mode (docs/backup-dr.md).

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT c.organization_id, 'composition', to_jsonb(c),
       'the legacy table composition is dropped (AST-05, WP-743); the child contract is ci.parent_ci_id '
       || 'with child_mode, repaired through PATCH /api/v1/cis/{id}', '000253'
FROM composition c
ORDER BY c.created_at, c.id;

INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT a.organization_id, 'asset', jsonb_build_object('id', a.id, 'parent_asset_id', a.parent_asset_id),
       'the unused column asset.parent_asset_id is dropped (AST-05, WP-743)', '000253'
FROM asset a
WHERE a.parent_asset_id IS NOT NULL
ORDER BY a.id;

DROP TABLE composition;
DROP FUNCTION reject_composition_cycle();
ALTER TABLE asset DROP COLUMN parent_asset_id;
