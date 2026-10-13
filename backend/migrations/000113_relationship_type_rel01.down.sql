-- Migration 000113 down: the former field names; impact_participation
-- from impact_direction (none = false).

ALTER TABLE relationship_type DROP CONSTRAINT relationship_type_direction_impact_check;
ALTER TABLE relationship_type DROP CONSTRAINT relationship_type_impact_direction_check;
ALTER TABLE relationship_type DROP CONSTRAINT relationship_type_category_check;
ALTER TABLE relationship_type ALTER COLUMN category DROP NOT NULL;
ALTER TABLE relationship_type ALTER COLUMN category DROP DEFAULT;

ALTER TABLE relationship_type RENAME COLUMN redundant TO impact_participation;
ALTER TABLE relationship_type ALTER COLUMN impact_participation SET DEFAULT true;
UPDATE relationship_type SET impact_participation = (impact_direction <> 'none');
ALTER TABLE relationship_type DROP COLUMN impact_direction;

ALTER TABLE relationship_type ALTER COLUMN directed DROP DEFAULT;
ALTER TABLE relationship_type ALTER COLUMN directed TYPE text USING (CASE WHEN directed THEN 'directed' ELSE 'undirected' END);
ALTER TABLE relationship_type RENAME COLUMN directed TO direction;
ALTER TABLE relationship_type ALTER COLUMN direction SET DEFAULT 'directed';
ALTER TABLE relationship_type ADD CONSTRAINT relationship_type_direction_check
    CHECK (direction IN ('directed', 'undirected'));

ALTER TABLE relationship_type RENAME COLUMN allowed_target_types TO target_ci_types;
ALTER TABLE relationship_type RENAME COLUMN allowed_source_types TO source_ci_types;
ALTER TABLE relationship_type RENAME COLUMN reverse_name TO reverse_label;
ALTER TABLE relationship_type RENAME COLUMN display_name TO forward_label;
