-- Migration 000113: relationship_type after REL-01 and CH27 (WP-127, E-18).
--
-- Field contract: display_name, reverse_name, directed (bool),
-- category network | power | hosting | logical | physical,
-- impact_direction source_depends_on_target | target_depends_on_source |
-- none | undirected, redundant, allowed_source_types,
-- allowed_target_types. Existing columns are renamed or retyped in place
-- (the column order stays); impact_participation becomes redundant, its
-- meaning moves to impact_direction (none = no impact). The system seeds
-- get their impact semantics; connected_to is undirected.

ALTER TABLE relationship_type RENAME COLUMN forward_label TO display_name;
ALTER TABLE relationship_type RENAME COLUMN reverse_label TO reverse_name;
ALTER TABLE relationship_type RENAME COLUMN source_ci_types TO allowed_source_types;
ALTER TABLE relationship_type RENAME COLUMN target_ci_types TO allowed_target_types;

ALTER TABLE relationship_type DROP CONSTRAINT relationship_type_direction_check;
ALTER TABLE relationship_type ALTER COLUMN direction DROP DEFAULT;
ALTER TABLE relationship_type ALTER COLUMN direction TYPE boolean USING (direction = 'directed');
ALTER TABLE relationship_type RENAME COLUMN direction TO directed;
ALTER TABLE relationship_type ALTER COLUMN directed SET DEFAULT true;

ALTER TABLE relationship_type ADD COLUMN impact_direction text;
UPDATE relationship_type SET impact_direction = CASE
    WHEN NOT directed THEN 'undirected'
    WHEN impact_participation THEN 'source_depends_on_target'
    ELSE 'none'
END;
ALTER TABLE relationship_type RENAME COLUMN impact_participation TO redundant;
ALTER TABLE relationship_type ALTER COLUMN redundant SET DEFAULT false;
UPDATE relationship_type SET redundant = false;

UPDATE relationship_type SET category = CASE category
    WHEN 'network' THEN 'network'
    WHEN 'power' THEN 'power'
    WHEN 'infrastructure' THEN 'hosting'
    WHEN 'storage' THEN 'hosting'
    WHEN 'hosting' THEN 'hosting'
    WHEN 'composition' THEN 'physical'
    WHEN 'location' THEN 'physical'
    WHEN 'physical' THEN 'physical'
    ELSE 'logical'
END;
ALTER TABLE relationship_type ALTER COLUMN category SET DEFAULT 'logical';
ALTER TABLE relationship_type ALTER COLUMN category SET NOT NULL;

-- Impact semantics of the system seeds (CH27, E-18).
UPDATE relationship_type t SET directed = s.directed, category = s.category,
    impact_direction = s.impact_direction, redundant = s.redundant
FROM (VALUES
    ('connected_to', false, 'network', 'undirected', false),
    ('uplink_to', true, 'network', 'source_depends_on_target', false),
    ('powered_by', true, 'power', 'source_depends_on_target', true),
    ('powers', true, 'power', 'target_depends_on_source', true),
    ('hosted_on', true, 'hosting', 'source_depends_on_target', false),
    ('runs_on', true, 'hosting', 'source_depends_on_target', false),
    ('stores', true, 'hosting', 'target_depends_on_source', false),
    ('member_of_cluster', true, 'logical', 'source_depends_on_target', true),
    ('depends_on', true, 'logical', 'source_depends_on_target', false),
    ('uses', true, 'logical', 'source_depends_on_target', false),
    ('used_by', true, 'logical', 'target_depends_on_source', false),
    ('member_of', true, 'logical', 'none', false),
    ('monitors', true, 'logical', 'none', false),
    ('backs_up', true, 'logical', 'none', false),
    ('backed_up_by', true, 'logical', 'none', false),
    ('managed_by', true, 'logical', 'none', false),
    ('manages', true, 'logical', 'none', false),
    ('assigned_to', true, 'logical', 'none', false),
    ('contains', true, 'physical', 'target_depends_on_source', false),
    ('contained_by', true, 'physical', 'source_depends_on_target', false),
    ('parent_of', true, 'physical', 'target_depends_on_source', false),
    ('child_of', true, 'physical', 'source_depends_on_target', false),
    ('mounted_in', true, 'physical', 'source_depends_on_target', false),
    ('located_in', true, 'physical', 'none', false)
) AS s(key, directed, category, impact_direction, redundant)
WHERE t.organization_id IS NULL AND t.key = s.key;

ALTER TABLE relationship_type ALTER COLUMN impact_direction SET DEFAULT 'source_depends_on_target';
ALTER TABLE relationship_type ALTER COLUMN impact_direction SET NOT NULL;
ALTER TABLE relationship_type ADD CONSTRAINT relationship_type_category_check
    CHECK (category IN ('network', 'power', 'hosting', 'logical', 'physical'));
ALTER TABLE relationship_type ADD CONSTRAINT relationship_type_impact_direction_check
    CHECK (impact_direction IN ('source_depends_on_target', 'target_depends_on_source', 'none', 'undirected'));
-- An undirected type has no impact direction of its own, a directed one
-- is not undirected.
ALTER TABLE relationship_type ADD CONSTRAINT relationship_type_direction_impact_check
    CHECK (directed = (impact_direction <> 'undirected'));
