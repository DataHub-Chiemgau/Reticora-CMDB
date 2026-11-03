-- Down of 000202: the constraints and the two global seeds go away; the
-- normalized cardinality and category of organization rows stay.

ALTER TABLE relationship_type DROP CONSTRAINT relationship_type_undirected_network_check;
ALTER TABLE relationship_type DROP CONSTRAINT relationship_type_cardinality_check;

DELETE FROM relationship_type WHERE organization_id IS NULL AND key IN ('monitored_by', 'belongs_to');
