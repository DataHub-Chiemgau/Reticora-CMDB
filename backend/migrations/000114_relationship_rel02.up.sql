-- Migration 000114: ci_relationship after REL-02 (WP-128).
--
-- Interface endpoints (source_interface_id, target_interface_id: an
-- interface of the edge's CI), last_confirmed_at, verified_by and
-- verified_at; confidence is a column with a check (0–1) and takes over
-- the value discovery stored in attributes.confidence. Uniqueness per
-- (org, source, target, type, interfaces); indexes (org, source) and
-- (org, target). provenance is the column source, metadata the column
-- attributes.

ALTER TABLE ci_relationship
    ADD COLUMN source_interface_id uuid REFERENCES network_interface (id) ON DELETE CASCADE,
    ADD COLUMN target_interface_id uuid REFERENCES network_interface (id) ON DELETE CASCADE,
    ADD COLUMN last_confirmed_at timestamptz,
    ADD COLUMN verified_by uuid REFERENCES app_user (id) ON DELETE SET NULL,
    ADD COLUMN verified_at timestamptz;

ALTER TABLE ci_relationship DISABLE TRIGGER trg_ci_relationship_updated_at;
UPDATE ci_relationship
SET confidence = least(greatest((attributes ->> 'confidence')::numeric, 0), 1),
    attributes = attributes - 'confidence'
WHERE jsonb_typeof(attributes -> 'confidence') = 'number';
UPDATE ci_relationship SET attributes = attributes - 'confidence' WHERE attributes ? 'confidence';
UPDATE ci_relationship SET last_confirmed_at = last_seen_at WHERE last_seen_at IS NOT NULL;
ALTER TABLE ci_relationship ENABLE TRIGGER trg_ci_relationship_updated_at;

ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_confidence_check
    CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1));
ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_verified_check
    CHECK (verified_by IS NULL OR verified_at IS NOT NULL);

-- An interface endpoint belongs to the CI at that end of the edge.
CREATE FUNCTION ci_relationship_interfaces() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.source_interface_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM network_interface WHERE id = NEW.source_interface_id AND ci_id = NEW.source_ci_id) THEN
        RAISE EXCEPTION 'source interface % is not an interface of CI %', NEW.source_interface_id, NEW.source_ci_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ci_relationship_source_interface';
    END IF;
    IF NEW.target_interface_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM network_interface WHERE id = NEW.target_interface_id AND ci_id = NEW.target_ci_id) THEN
        RAISE EXCEPTION 'target interface % is not an interface of CI %', NEW.target_interface_id, NEW.target_ci_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ci_relationship_target_interface';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ci_relationship_interfaces
    BEFORE INSERT OR UPDATE OF source_ci_id, target_ci_id, source_interface_id, target_interface_id ON ci_relationship
    FOR EACH ROW EXECUTE FUNCTION ci_relationship_interfaces();

ALTER TABLE ci_relationship DROP CONSTRAINT ci_relationship_organization_id_source_ci_id_target_ci_id_r_key;
CREATE UNIQUE INDEX uq_ci_relationship_edge ON ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type,
    COALESCE(source_interface_id, '00000000-0000-0000-0000-000000000000'::uuid),
    COALESCE(target_interface_id, '00000000-0000-0000-0000-000000000000'::uuid));

DROP INDEX idx_ci_rel_source;
DROP INDEX idx_ci_rel_target;
CREATE INDEX idx_ci_rel_org_source ON ci_relationship (organization_id, source_ci_id);
CREATE INDEX idx_ci_rel_org_target ON ci_relationship (organization_id, target_ci_id);
CREATE INDEX idx_ci_rel_source_interface ON ci_relationship (source_interface_id) WHERE source_interface_id IS NOT NULL;
CREATE INDEX idx_ci_rel_target_interface ON ci_relationship (target_interface_id) WHERE target_interface_id IS NOT NULL;
