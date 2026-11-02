-- WP-396 (CI-01, TEN-09; F-144): a CI has a system type or a type of its own
-- organization.
--
-- The foreign key ci_ci_type_id_fkey has one column and, like every foreign
-- key, bypasses row level security: POST /cis of organization B with the
-- private type of organization A answered 201, the row referenced the foreign
-- type (its rules did not apply), and 201 against 422 for an unknown id told
-- B that the type exists. The CI repository now reads the chosen type under
-- the writer's row level security (422 on ci_type_id); the constraint trigger
-- below holds the rule for every write path (API, import, discovery, bulk,
-- type change, unmerge), its violation 23514 ci_type_organization is reported
-- as 422 on ci_type_id as well.
--
-- The trigger function runs with the rights of the writer: under row level
-- security a private type of another organization is invisible, so it
-- fails the check like a type whose organization differs.

-- Existing rows must already follow the rule; the migration stops instead of
-- changing CIs (they have to be corrected by hand).
DO $$
DECLARE
    foreign_typed bigint;
BEGIN
    SELECT count(*) INTO foreign_typed
      FROM ci c JOIN ci_type t ON t.id = c.ci_type_id
     WHERE t.organization_id IS NOT NULL AND t.organization_id <> c.organization_id;
    IF foreign_typed > 0 THEN
        RAISE EXCEPTION '% CIs have a CI type of another organization', foreign_typed;
    END IF;
END
$$;

CREATE FUNCTION ci_check_type_organization() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM ci_type t
        WHERE t.id = NEW.ci_type_id AND (t.organization_id IS NULL OR t.organization_id = NEW.organization_id)
    ) THEN
        RAISE EXCEPTION 'the type of CI % is no system type and no type of its organization', NEW.id
            USING ERRCODE = '23514', CONSTRAINT = 'ci_type_organization';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER trg_ci_type_organization AFTER INSERT OR UPDATE OF ci_type_id, organization_id ON ci
    FOR EACH ROW EXECUTE FUNCTION ci_check_type_organization();
