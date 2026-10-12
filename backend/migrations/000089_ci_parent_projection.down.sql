DROP VIEW ci_structure_edge;
DROP TRIGGER trg_ci_parent_cycle ON ci;
DROP FUNCTION ci_check_parent_cycle();
ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_not_structural;

-- Restore the quarantined structural relationships.
INSERT INTO ci_relationship
SELECT r.* FROM migration_quarantine q,
     jsonb_populate_record(NULL::ci_relationship, q.row_data) r
WHERE q.migration = '000089' AND q.source_table = 'ci_relationship'
ON CONFLICT DO NOTHING;
-- Containment set after the migration becomes a contains row.
INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
SELECT organization_id, parent_ci_id, id, 'contains', 'manual'
FROM ci WHERE parent_ci_id IS NOT NULL
ON CONFLICT DO NOTHING;
DELETE FROM migration_quarantine WHERE migration = '000089';

DROP INDEX idx_ci_parent;
ALTER TABLE ci DROP CONSTRAINT ci_parent_not_self;
ALTER TABLE ci DROP CONSTRAINT ci_parent_ci_fkey;
ALTER TABLE ci DROP COLUMN parent_ci_id;
