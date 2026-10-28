-- WP-079 (DB-05, REL-01, AST-05, CH27): structure is stored once and
-- projected as relationships.
--
-- * ci.parent_ci_id holds physical containment (chassis → blade); a
--   trigger rejects cycles.
-- * contains, contained_by, parent_of and child_of are no longer stored:
--   existing rows become ci.parent_ci_id (one parent per child, the oldest
--   row wins). mounted_in and located_in follow from rack_mount and
--   ci.location_id. All such rows move to migration_quarantine (E-26) and
--   the down migration restores them.
-- * ci_structure_edge projects contains (CI → CI), mounted_in (CI → rack)
--   and located_in (CI → location).

-- Databases that applied 000056 before E-26 have no migration_quarantine,
-- and this is the first migration that writes to it (F-378, E-100), unless
-- they applied 000066 in its form since WP-560, which creates it as well.
-- Create it here exactly as 000056 does; on every other database nothing
-- happens.
-- The event trigger of 000078 hands the table to reticora_owner; the grants
-- are those the default privileges of 000056 give a table created there.
DO $$
BEGIN
    IF to_regclass('public.migration_quarantine') IS NULL THEN
        CREATE TABLE migration_quarantine (
            id              BIGSERIAL PRIMARY KEY,
            organization_id UUID NOT NULL,
            source_table    TEXT NOT NULL,
            row_data        JSONB NOT NULL,
            reason          TEXT NOT NULL,
            migration       TEXT NOT NULL,
            quarantined_at  TIMESTAMPTZ NOT NULL DEFAULT now()
        );
        CREATE INDEX idx_migration_quarantine_org ON migration_quarantine (organization_id, source_table);
        ALTER TABLE migration_quarantine ENABLE ROW LEVEL SECURITY;
        ALTER TABLE migration_quarantine FORCE ROW LEVEL SECURITY;
        CREATE POLICY migration_quarantine_isolation ON migration_quarantine
            USING (organization_id = current_setting('app.org_id')::UUID)
            WITH CHECK (organization_id = current_setting('app.org_id')::UUID);
        GRANT SELECT, INSERT, UPDATE, DELETE ON migration_quarantine TO reticora_app;
        GRANT USAGE, SELECT ON SEQUENCE migration_quarantine_id_seq TO reticora_app;
    END IF;
END
$$;

ALTER TABLE ci ADD COLUMN parent_ci_id uuid;
ALTER TABLE ci ADD CONSTRAINT ci_parent_ci_fkey FOREIGN KEY (parent_ci_id, organization_id)
    REFERENCES ci (id, organization_id) ON DELETE SET NULL (parent_ci_id);
ALTER TABLE ci ADD CONSTRAINT ci_parent_not_self CHECK (parent_ci_id IS NULL OR parent_ci_id <> id);
CREATE INDEX idx_ci_parent ON ci (parent_ci_id) WHERE parent_ci_id IS NOT NULL;

WITH structural AS (
    SELECT organization_id, created_at,
           CASE WHEN rel_type IN ('contains', 'parent_of') THEN target_ci_id ELSE source_ci_id END AS child,
           CASE WHEN rel_type IN ('contains', 'parent_of') THEN source_ci_id ELSE target_ci_id END AS parent
    FROM ci_relationship
    WHERE rel_type IN ('contains', 'parent_of', 'contained_by', 'child_of') AND source_ci_id <> target_ci_id
), chosen AS (
    SELECT DISTINCT ON (child) child, parent, organization_id FROM structural ORDER BY child, created_at
)
UPDATE ci SET parent_ci_id = chosen.parent
FROM chosen
WHERE ci.id = chosen.child AND ci.organization_id = chosen.organization_id;

WITH moved AS (
    DELETE FROM ci_relationship
    WHERE rel_type IN ('contains', 'contained_by', 'parent_of', 'child_of', 'mounted_in', 'located_in')
    RETURNING *
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ci_relationship', to_jsonb(moved),
       'structural relationship is projected (DB-05, CH27): containment is ci.parent_ci_id, '
       || 'mounted_in follows from rack_mount, located_in from ci.location_id', '000089'
FROM moved;

ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_not_structural
    CHECK (rel_type <> ALL (ARRAY['contains', 'contained_by', 'parent_of', 'child_of', 'mounted_in', 'located_in']));

-- No CI may contain itself, directly or through its parents (AST-05).
CREATE FUNCTION ci_check_parent_cycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_ci_id IS NULL THEN
        RETURN NEW;
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
CREATE TRIGGER trg_ci_parent_cycle BEFORE INSERT OR UPDATE OF parent_ci_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_check_parent_cycle();

CREATE VIEW ci_structure_edge WITH (security_invoker = true) AS
    SELECT c.organization_id, 'contains'::text AS rel_type, c.parent_ci_id AS source_ci_id,
           c.id AS target_ci_id, NULL::uuid AS target_location_id
    FROM ci c
    WHERE c.parent_ci_id IS NOT NULL AND c.deleted_at IS NULL
    UNION ALL
    SELECT m.organization_id, 'mounted_in', m.ci_id, NULL::uuid, m.rack_id
    FROM rack_mount m
    UNION ALL
    SELECT c.organization_id, 'located_in', c.id, NULL::uuid, c.location_id
    FROM ci c
    WHERE c.location_id IS NOT NULL AND c.deleted_at IS NULL;
GRANT SELECT ON ci_structure_edge TO reticora_app;
