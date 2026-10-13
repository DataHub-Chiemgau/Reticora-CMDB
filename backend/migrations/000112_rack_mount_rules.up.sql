-- Migration 000112: rack rules of RCK-01 (WP-126).
--
-- The exclusion constraint is face-aware: front occupies [1,2), rear
-- [2,3), both [1,3) of the face range, so a mount on both faces collides
-- with front and rear mounts at the same height units (until now both was
-- left out). Child CIs of a composition (AST-05, ci.parent_ci_id) are not
-- mounted separately: their parent is.

CREATE FUNCTION rack_face_range(face text) RETURNS int4range
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE face WHEN 'front' THEN int4range(1, 2) WHEN 'rear' THEN int4range(2, 3) ELSE int4range(1, 3) END
$$;

DO $$
DECLARE
    clash record;
BEGIN
    SELECT a.id AS a, b.id AS b, a.rack_id INTO clash
    FROM rack_mount a JOIN rack_mount b ON a.rack_id = b.rack_id AND a.id < b.id
    WHERE int4range(a.position_u, a.position_u + a.height_u) && int4range(b.position_u, b.position_u + b.height_u)
      AND rack_face_range(a.face) && rack_face_range(b.face)
    LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'rack % has overlapping mounts % and % (a mount on both faces overlaps a front or rear mount); move one before migrating',
            clash.rack_id, clash.a, clash.b;
    END IF;
END
$$;

ALTER TABLE rack_mount DROP CONSTRAINT rack_mount_no_overlap;
ALTER TABLE rack_mount ADD CONSTRAINT rack_mount_no_overlap EXCLUDE USING gist (
    rack_id WITH =,
    int4range(position_u, position_u + height_u) WITH &&,
    rack_face_range(face) WITH &&);

CREATE FUNCTION rack_mount_not_composition_child() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'rack_mount' THEN
        IF EXISTS (SELECT 1 FROM ci WHERE id = NEW.ci_id AND parent_ci_id IS NOT NULL) THEN
            RAISE EXCEPTION 'CI % is part of a composition; its parent is mounted (RCK-01)', NEW.ci_id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'rack_mount_composition_child';
        END IF;
    ELSIF NEW.parent_ci_id IS NOT NULL AND EXISTS (SELECT 1 FROM rack_mount WHERE ci_id = NEW.id) THEN
        RAISE EXCEPTION 'CI % is mounted in a rack and cannot become part of a composition (RCK-01)', NEW.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'rack_mount_composition_child';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_rack_mount_composition_child BEFORE INSERT OR UPDATE OF ci_id ON rack_mount
    FOR EACH ROW EXECUTE FUNCTION rack_mount_not_composition_child();
CREATE TRIGGER trg_ci_mounted_composition_child BEFORE UPDATE OF parent_ci_id ON ci
    FOR EACH ROW EXECUTE FUNCTION rack_mount_not_composition_child();
