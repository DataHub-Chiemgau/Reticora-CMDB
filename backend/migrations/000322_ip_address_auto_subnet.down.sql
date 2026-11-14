-- Down of 000322: the automatic subnet of IP addresses goes (WP-912). The
-- assignment of the existing addresses stays (data, not schema).
DROP TRIGGER trg_subnet_release_addresses ON subnet;
DROP FUNCTION subnet_release_addresses();

-- The refusal of a shrunk prefix that leaves addresses outside, as of 000299.
CREATE FUNCTION subnet_cidr_keeps_addresses() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    outside boolean;
BEGIN
    SELECT EXISTS (SELECT 1 FROM ip_address a
                    WHERE a.organization_id = NEW.organization_id AND a.subnet_id = NEW.id
                      AND NOT (a.address <<= NEW.cidr))
      INTO outside;
    PERFORM app_scope_restore(previous);
    IF outside THEN
        RAISE EXCEPTION 'addresses of the subnet lie outside its new prefix'
            USING ERRCODE = '23514', CONSTRAINT = 'subnet_cidr_addresses';
    END IF;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_subnet_cidr_keeps_addresses AFTER UPDATE OF cidr ON subnet
    FOR EACH ROW WHEN (OLD.cidr IS DISTINCT FROM NEW.cidr)
    EXECUTE FUNCTION subnet_cidr_keeps_addresses();

-- subnet_overlaps and subnet_place as of 000300.
CREATE OR REPLACE FUNCTION subnet_overlaps(p_subnet uuid) RETURNS uuid[]
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

CREATE OR REPLACE FUNCTION subnet_place() RETURNS trigger
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

DROP FUNCTION ipam_longest_prefix_subnet(uuid, uuid, inet);
