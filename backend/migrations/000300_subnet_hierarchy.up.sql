-- Migration 000300: hierarchy of subnets (IPM-02, WP-901, E-210).
--
-- * subnet.parent_id is the narrowest subnet of the same organization and
--   VRF that contains the subnet (master/nested); it is stored and computed
--   on write (E-210 option 1), never written through the API. is_pool marks
--   a container without host addresses of its own.
-- * subnet_place() (AFTER INSERT, AFTER UPDATE OF cidr, vrf_id, is_pool,
--   section_id) places the subnet under its parent, hangs the subnets that
--   now lie in it below it and, when it leaves its VRF or its prefix
--   shrinks, gives its former children the narrowest remaining container.
--   subnet_unplace() (AFTER DELETE) gives the children of a deleted subnet
--   its former parent. Both run under pg_advisory_xact_lock per
--   (organization, VRF), both VRFs in a fixed order on a VRF change, and
--   read all subnets of the VRF with the scope widened (000139), whatever
--   the writer sees. (AFTER DELETE instead of BEFORE DELETE: a BEFORE
--   trigger that updates rows the same DELETE statement removes later makes
--   PostgreSQL refuse that statement.)
-- * strict_mode of the section (WP-900): a true nesting (master/nested) is
--   allowed in every section without warning. CIDR prefixes never
--   intersect without one containing the other, so an overlap is an
--   address space used twice: a parent that is no pool holds addresses of
--   its own inside the subnet, or the subnet, when no pool, holds
--   addresses of its own inside one of its children (a child of a pool
--   always passes). In a strict section that is 409 cidr
--   (subnet_overlap_strict), otherwise a warning. A subnet with addresses
--   of its own cannot become a pool (subnet_pool_addresses, 409 is_pool).
-- * The existing subnets get their parent set-based per organization and
--   VRF.

-- UNIQUE (id, organization_id) for the self reference (Konvention 19);
-- migration 000287 created it, the guard keeps older states working.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'subnet'::regclass AND conname = 'subnet_id_organization_key') THEN
        ALTER TABLE subnet ADD CONSTRAINT subnet_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

ALTER TABLE subnet
    ADD COLUMN parent_id uuid NULL,
    ADD COLUMN is_pool boolean NOT NULL DEFAULT false;
ALTER TABLE subnet ADD CONSTRAINT subnet_parent_fkey FOREIGN KEY (parent_id, organization_id)
    REFERENCES subnet (id, organization_id) ON DELETE SET NULL (parent_id);
CREATE INDEX idx_subnet_org_vrf_parent ON subnet (organization_id, vrf_id, parent_id);

-- The narrowest subnet of the VRF that contains p_cidr, p_self excluded.
-- The caller widens the scope.
CREATE FUNCTION subnet_narrowest(p_org uuid, p_vrf uuid, p_cidr cidr, p_self uuid) RETURNS uuid
LANGUAGE sql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT s.id FROM subnet s
     WHERE s.organization_id = p_org AND s.vrf_id = p_vrf AND s.id <> p_self AND s.cidr >> p_cidr
     ORDER BY masklen(s.cidr) DESC
     LIMIT 1
$$;

-- The advisory lock of the tree of a VRF.
CREATE FUNCTION subnet_tree_lock(p_org uuid, p_vrf uuid) RETURNS void
LANGUAGE sql
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT pg_advisory_xact_lock(hashtextextended('subnet_tree:' || p_org::text || ':' || p_vrf::text, 0))
$$;

-- The conflict partners of a subnet under the overlap rule (IPM-02): CIDR
-- prefixes never intersect without one containing the other, so a true
-- nesting (master/nested) is no overlap. Two subnets overlap when the
-- address space of one is used twice: the parent, when it is no pool,
-- holds addresses of its own inside the subnet; or the subnet, when it is
-- no pool, holds addresses of its own inside one of its children. A pool
-- (no host addresses of its own) takes every child. The caller widens the
-- scope.
CREATE FUNCTION subnet_overlap_partners(p_subnet uuid) RETURNS uuid[]
LANGUAGE sql STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT COALESCE(array_agg(x.id ORDER BY x.id), '{}') FROM (
        SELECT p.id FROM subnet s JOIN subnet p ON p.id = s.parent_id AND p.organization_id = s.organization_id
         WHERE s.id = p_subnet AND NOT p.is_pool
           AND EXISTS (SELECT 1 FROM ip_address a
                        WHERE a.organization_id = p.organization_id AND a.subnet_id = p.id AND a.address <<= s.cidr)
        UNION
        SELECT c.id FROM subnet s JOIN subnet c ON c.organization_id = s.organization_id AND c.vrf_id = s.vrf_id
                                                AND c.parent_id = s.id
         WHERE s.id = p_subnet AND NOT s.is_pool
           AND EXISTS (SELECT 1 FROM ip_address a
                        WHERE a.organization_id = s.organization_id AND a.subnet_id = s.id AND a.address <<= c.cidr)) x
$$;

-- subnet_overlaps reads the conflict partners of a subnet for the warnings
-- outside strict sections (the write path names only the ids the caller
-- sees), with the scope widened.
CREATE FUNCTION subnet_overlaps(p_subnet uuid) RETURNS uuid[]
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    partners uuid[];
BEGIN
    partners := subnet_overlap_partners(p_subnet);
    PERFORM app_scope_restore(previous);
    RETURN partners;
EXCEPTION WHEN OTHERS THEN
    PERFORM app_scope_restore(previous);
    RAISE;
END
$$;

CREATE FUNCTION subnet_place() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    parent uuid;
    is_strict boolean;
    partner uuid;
    has_addresses boolean;
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.vrf_id IS DISTINCT FROM NEW.vrf_id THEN
        PERFORM subnet_tree_lock(NEW.organization_id, v)
           FROM unnest(ARRAY[OLD.vrf_id, NEW.vrf_id]) AS v ORDER BY v::text;
    ELSE
        PERFORM subnet_tree_lock(NEW.organization_id, NEW.vrf_id);
    END IF;
    previous := app_scope_widen();
    BEGIN
        IF TG_OP = 'UPDATE' AND NEW.is_pool AND NOT OLD.is_pool THEN
            SELECT EXISTS (SELECT 1 FROM ip_address a WHERE a.organization_id = NEW.organization_id AND a.subnet_id = NEW.id)
              INTO has_addresses;
            IF has_addresses THEN
                RAISE EXCEPTION 'a subnet with addresses cannot become a pool'
                    USING ERRCODE = '23514', CONSTRAINT = 'subnet_pool_addresses';
            END IF;
        END IF;
        -- Every former child takes its narrowest container anew: one that
        -- left the subnet (VRF left or prefix shrunk) the narrowest
        -- remaining one, one that stays in a widened subnet a subnet that
        -- now lies between them (10.1.1.0/24 under 10.0.0.0/10 when its
        -- parent 10.1.0.0/16 becomes 10.0.0.0/9).
        IF TG_OP = 'UPDATE' THEN
            UPDATE subnet c SET parent_id = n.parent
              FROM (SELECT k.id, subnet_narrowest(k.organization_id, k.vrf_id, k.cidr, k.id) AS parent
                      FROM subnet k WHERE k.organization_id = NEW.organization_id AND k.parent_id = NEW.id) n
             WHERE c.id = n.id AND c.organization_id = NEW.organization_id AND n.parent IS DISTINCT FROM NEW.id;
        END IF;
        -- The subnet under its parent.
        parent := subnet_narrowest(NEW.organization_id, NEW.vrf_id, NEW.cidr, NEW.id);
        UPDATE subnet SET parent_id = parent
         WHERE id = NEW.id AND organization_id = NEW.organization_id AND parent_id IS DISTINCT FROM parent;
        -- Subnets of the VRF that now lie in it hang below it: those whose
        -- narrowest container was the subnet's parent (prefixes nest).
        UPDATE subnet c SET parent_id = NEW.id
         WHERE c.organization_id = NEW.organization_id AND c.vrf_id = NEW.vrf_id AND c.id <> NEW.id
           AND c.cidr << NEW.cidr AND c.parent_id IS NOT DISTINCT FROM parent;
        -- strict_mode: no overlap (subnet_overlap_partners); a true
        -- nesting and every child of a pool pass.
        SELECT sec.strict_mode INTO is_strict FROM ipam_section sec
         WHERE sec.id = NEW.section_id AND sec.organization_id = NEW.organization_id;
        IF is_strict THEN
            partner := (subnet_overlap_partners(NEW.id))[1];
            IF partner IS NOT NULL THEN
                RAISE EXCEPTION 'the subnet overlaps another subnet of a strict section'
                    USING ERRCODE = '23514', CONSTRAINT = 'subnet_overlap_strict', DETAIL = partner::text;
            END IF;
        END IF;
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE FUNCTION subnet_unplace() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
BEGIN
    PERFORM subnet_tree_lock(OLD.organization_id, OLD.vrf_id);
    previous := app_scope_widen();
    BEGIN
        -- The children of the deleted subnet (parent_id cleared by the
        -- foreign key, or still naming it) take the narrowest remaining
        -- container: the former parent.
        UPDATE subnet c SET parent_id = subnet_narrowest(c.organization_id, c.vrf_id, c.cidr, c.id)
         WHERE c.organization_id = OLD.organization_id AND c.vrf_id = OLD.vrf_id
           AND (c.parent_id IS NULL OR c.parent_id = OLD.id) AND c.cidr << OLD.cidr;
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

CREATE TRIGGER trg_subnet_place_insert AFTER INSERT ON subnet
    FOR EACH ROW EXECUTE FUNCTION subnet_place();
CREATE TRIGGER trg_subnet_place_update AFTER UPDATE OF cidr, vrf_id, is_pool, section_id ON subnet
    FOR EACH ROW
    WHEN (OLD.cidr IS DISTINCT FROM NEW.cidr OR OLD.vrf_id IS DISTINCT FROM NEW.vrf_id
          OR OLD.is_pool IS DISTINCT FROM NEW.is_pool OR OLD.section_id IS DISTINCT FROM NEW.section_id)
    EXECUTE FUNCTION subnet_place();
CREATE TRIGGER trg_subnet_unplace AFTER DELETE ON subnet
    FOR EACH ROW EXECUTE FUNCTION subnet_unplace();

-- Backfill: the narrowest container of every subnet, set-based per
-- organization and VRF, without touching updated_at.
ALTER TABLE subnet DISABLE TRIGGER trg_subnet_updated_at;
UPDATE subnet s SET parent_id = (
    SELECT p.id FROM subnet p
     WHERE p.organization_id = s.organization_id AND p.vrf_id = s.vrf_id AND p.id <> s.id AND p.cidr >> s.cidr
     ORDER BY masklen(p.cidr) DESC
     LIMIT 1);
ALTER TABLE subnet ENABLE TRIGGER trg_subnet_updated_at;
