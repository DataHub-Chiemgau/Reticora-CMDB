-- Migration 000114 down: no interface endpoints, confirmation or
-- verification actor; the confidence goes back into attributes.

DROP INDEX idx_ci_rel_target_interface;
DROP INDEX idx_ci_rel_source_interface;
DROP INDEX idx_ci_rel_org_target;
DROP INDEX idx_ci_rel_org_source;
CREATE INDEX idx_ci_rel_source ON ci_relationship (source_ci_id);
CREATE INDEX idx_ci_rel_target ON ci_relationship (target_ci_id);

DROP INDEX uq_ci_relationship_edge;
DELETE FROM ci_relationship r USING ci_relationship o
WHERE r.organization_id = o.organization_id AND r.source_ci_id = o.source_ci_id AND r.target_ci_id = o.target_ci_id
  AND r.rel_type = o.rel_type AND r.id > o.id;
ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_organization_id_source_ci_id_target_ci_id_r_key
    UNIQUE (organization_id, source_ci_id, target_ci_id, rel_type);

DROP TRIGGER trg_ci_relationship_interfaces ON ci_relationship;
DROP FUNCTION ci_relationship_interfaces();
ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_verified_check;
ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_confidence_check;

ALTER TABLE ci_relationship DISABLE TRIGGER trg_ci_relationship_updated_at;
UPDATE ci_relationship SET attributes = attributes || jsonb_build_object('confidence', confidence)
WHERE confidence IS NOT NULL AND source <> 'manual';
ALTER TABLE ci_relationship ENABLE TRIGGER trg_ci_relationship_updated_at;

ALTER TABLE ci_relationship
    DROP COLUMN verified_at,
    DROP COLUMN verified_by,
    DROP COLUMN last_confirmed_at,
    DROP COLUMN target_interface_id,
    DROP COLUMN source_interface_id;
