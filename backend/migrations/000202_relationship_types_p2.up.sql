-- Migration 000202: relationship types after REL-06 (WP-294, E-125).
--
-- 1. The system types monitored_by (the source is the monitored CI, no
--    impact) and belongs_to (the source depends on the target) as global
--    rows. Organization rows with these keys stay; List, GetByKey and the
--    impact resolve a key to the organization's row before the system row.
-- 2. Normalization of the organization rows: a cardinality outside the four
--    values becomes many_to_many, an undirected type outside the category
--    network gets the category network. The down migration keeps the
--    normalized values.
-- 3. relationship_type_cardinality_check and
--    relationship_type_undirected_network_check hold the contract that
--    relationshiptype.Type.Validate checks.
-- No new table or column; RLS and policies stay unchanged.

INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, directed, cardinality,
    category, impact_direction, redundant, is_system)
SELECT NULL, s.key, s.display_name, s.reverse_name, true, 'many_to_many', 'logical', s.impact_direction, false, true
FROM (VALUES
    ('monitored_by', 'monitored by', 'monitors', 'none'),
    ('belongs_to', 'belongs to', 'has member', 'source_depends_on_target')
) AS s(key, display_name, reverse_name, impact_direction)
WHERE NOT EXISTS (SELECT 1 FROM relationship_type t WHERE t.organization_id IS NULL AND t.key = s.key);

UPDATE relationship_type SET cardinality = 'many_to_many'
WHERE cardinality NOT IN ('one_to_one', 'one_to_many', 'many_to_one', 'many_to_many');
UPDATE relationship_type SET category = 'network'
WHERE impact_direction = 'undirected' AND category <> 'network';

ALTER TABLE relationship_type ADD CONSTRAINT relationship_type_cardinality_check
    CHECK (cardinality IN ('one_to_one', 'one_to_many', 'many_to_one', 'many_to_many'));
-- An undirected type (connected_to) is a network heuristic (IMP-03).
ALTER TABLE relationship_type ADD CONSTRAINT relationship_type_undirected_network_check
    CHECK (impact_direction <> 'undirected' OR category = 'network');
