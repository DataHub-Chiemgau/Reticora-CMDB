-- Composition integrity: a serialized parent asset must never become its own
-- ancestor (spec §47 "no invalid recursive ownership where forbidden").
--
-- Only asset-to-asset links can form a cycle: a composition child that is a CI
-- is a leaf, because compositions are always parented by an asset. The guard
-- therefore walks the parent chain upwards from the new parent and rejects the
-- write if it reaches the child again.
--
-- This is enforced in the database rather than only in the repository so that
-- every write path (API, import, workflow, future modules) is covered and the
-- check runs inside the same transaction as the insert.

CREATE OR REPLACE FUNCTION reject_composition_cycle() RETURNS TRIGGER AS $$
DECLARE
    ancestor UUID;
    depth INT := 0;
BEGIN
    IF NEW.child_asset_id IS NULL THEN
        RETURN NEW;
    END IF;

    IF NEW.child_asset_id = NEW.parent_asset_id THEN
        RAISE EXCEPTION 'composition cycle: an asset cannot be its own parent'
            USING ERRCODE = '23514';
    END IF;

    ancestor := NEW.parent_asset_id;
    WHILE ancestor IS NOT NULL LOOP
        depth := depth + 1;
        -- Defensive bound: a pre-existing cycle must not spin forever.
        IF depth > 100 THEN
            RAISE EXCEPTION 'composition ancestry deeper than supported limit'
                USING ERRCODE = '23514';
        END IF;

        SELECT c.parent_asset_id INTO ancestor
        FROM composition c
        WHERE c.child_asset_id = ancestor
          AND c.organization_id = NEW.organization_id
          AND (TG_OP = 'INSERT' OR c.id <> NEW.id);

        IF ancestor = NEW.child_asset_id THEN
            RAISE EXCEPTION 'composition cycle: asset % is already an ancestor of the requested parent',
                NEW.child_asset_id USING ERRCODE = '23514';
        END IF;
    END LOOP;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_composition_no_cycle ON composition;
CREATE TRIGGER trg_composition_no_cycle
    BEFORE INSERT OR UPDATE OF parent_asset_id, child_asset_id ON composition
    FOR EACH ROW EXECUTE FUNCTION reject_composition_cycle();
