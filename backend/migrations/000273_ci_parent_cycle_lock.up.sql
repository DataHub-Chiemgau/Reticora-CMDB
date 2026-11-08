-- WP-825 (ABN-03): the cycle check of ci.parent_ci_id serializes per
-- organization.
--
-- ci_check_parent_cycle (000089) read the parent path without a lock. With
-- A→B in place ("A has the parent B"), T1 setting B→C and T2 setting C→A
-- both passed: each read the path in a snapshot without the other's write,
-- and T1 waited for C at most in the foreign key check, after its own
-- cycle check. Both committed and stored A→B→C→A.
--
-- Like the location tree (location_tree, 000062) and the type tree
-- (ci_type_tree, 000197), an INSERT with a parent and a changed parent take
-- the organization's advisory lock before the recursive check. The trigger
-- function is VOLATILE: under READ COMMITTED its query reads the path after
-- the lock with a fresh snapshot, so the second writer sees the committed
-- parent of the first. SQLSTATE 23514 and the constraint ci_parent_cycle
-- stay (422 on parent_ci_id, backend/internal/ci/pg_repository.go). The
-- ingest does not write parent_ci_id (backend/internal/discovery), so the
-- lock does not touch NFR-03. Trigger trg_ci_parent_cycle, tables, RLS and
-- policies are unchanged.
CREATE OR REPLACE FUNCTION ci_check_parent_cycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_ci_id IS NULL THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' OR NEW.parent_ci_id IS DISTINCT FROM OLD.parent_ci_id THEN
        -- Parent changes of one organization run one after the other; the
        -- parent path is read after the lock with a fresh snapshot.
        PERFORM pg_advisory_xact_lock(hashtextextended('ci_parent_tree:' || NEW.organization_id::text, 0));
    END IF;
    IF EXISTS (
        WITH RECURSIVE up (id) AS (
            SELECT NEW.parent_ci_id
            UNION
            SELECT c.parent_ci_id FROM ci c JOIN up ON c.id = up.id WHERE c.parent_ci_id IS NOT NULL
        )
        SELECT 1 FROM up WHERE id = NEW.id
    ) THEN
        RAISE EXCEPTION 'CI % would contain itself', NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_parent_cycle';
    END IF;
    RETURN NEW;
END
$$;
