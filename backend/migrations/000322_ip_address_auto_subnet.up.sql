-- Migration 000322: automatic subnet of IP addresses (IPM-14, WP-912, E-210).
--
-- * ipam_longest_prefix_subnet(org, vrf, addr) is the narrowest subnet of
--   the VRF that is no pool and contains the address (longest prefix),
--   read with the scope widened (000139), whatever the writer sees; only
--   rows of org (and, under row level security, of app.org_id). The write
--   paths of the addresses call it inside their INSERT (and the discovery
--   sync inside its UPDATE) for an address without subnet_id: no
--   statement of its own per address.
-- * subnet_place (version of 000300) reassigns, after the overlap rule and
--   only on creation (also through allocate-subnet), a changed prefix, a
--   move into another VRF or a pool that became a subnet (not on a mere
--   change of the section), under the VRF lock and recorded as allocation:
--   - release: a shrunk prefix hands the addresses of the subnet now
--     outside it to their longest prefix (a remaining subnet or none) in
--     one UPDATE; a gateway mark goes with them
--     (trg_ip_address_gateway_leaves_subnet of 000299). The refusal of
--     000299 (subnet_cidr_keeps_addresses, 422 cidr) goes;
--   - takeover: the addresses of the VRF inside the subnet that belong to
--     the narrowest subnet without pool around it (the subnet they had by
--     longest prefix, also when a pool lies between) or to no subnet get
--     their longest prefix anew in one UPDATE. The subnet's own addresses
--     follow it into another VRF (trg_subnet_propagate_vrf of 000092).
--   Gateway marks of other subnets stay where they are. The overlap
--   partners of a subnet outside a strict section are noted before the
--   takeover (app.subnet_overlap_taken), so the warning overlap (E-247)
--   still names the parent whose addresses it took; subnet_overlaps
--   (version of 000300) reads them along.
-- * subnet_release_addresses (BEFORE DELETE) hands the addresses of a
--   deleted subnet to their longest prefix without it (the parent or no
--   subnet) before the row goes, in one UPDATE.
-- * Every reassignment runs with app.ipam_change_source = allocation, so
--   ipam_change (000302) records it per address set-based.
-- * The existing addresses without subnet get their longest prefix once,
--   through ipam_longest_prefix_subnet like every write path.

-- ─── 1. Longest prefix ───────────────────────────────────────────────────────

CREATE FUNCTION ipam_longest_prefix_subnet(p_org uuid, p_vrf uuid, p_addr inet) RETURNS uuid
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    org text := NULLIF(current_setting('app.org_id', true), '');
    found uuid;
BEGIN
    IF p_org IS NULL OR p_vrf IS NULL OR p_addr IS NULL THEN
        RETURN NULL;
    END IF;
    -- No exception block: it would open a subtransaction per call. An
    -- error aborts the (sub)transaction, which resets the settings.
    previous := app_scope_widen();
    -- A transaction without organization (maintenance, migration) reads
    -- the organization asked for; one with an organization only its own.
    IF org IS NULL THEN
        PERFORM set_config('app.org_id', p_org::text, true);
    END IF;
    SELECT s.id INTO found FROM subnet s
     WHERE s.organization_id = p_org AND s.vrf_id = p_vrf AND NOT s.is_pool AND s.cidr >>= p_addr
     ORDER BY masklen(s.cidr) DESC, s.id
     LIMIT 1;
    IF org IS NULL THEN
        PERFORM set_config('app.org_id', '', true);
    END IF;
    PERFORM app_scope_restore(previous);
    RETURN found;
END
$$;

-- reticora_owner does not bypass row level security (FORCE): the function
-- reads only rows of app.org_id.
ALTER FUNCTION ipam_longest_prefix_subnet(uuid, uuid, inet) OWNER TO reticora_owner;

-- ─── 2. Overlap warnings across a takeover ───────────────────────────────────

CREATE OR REPLACE FUNCTION subnet_overlaps(p_subnet uuid) RETURNS uuid[]
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[] := app_scope_widen();
    partners uuid[];
BEGIN
    -- The partners now, and those subnet_place noted before it took the
    -- addresses over (entries "subnet:partner,partner" separated by ";").
    SELECT COALESCE(array_agg(DISTINCT p ORDER BY p), '{}') INTO partners FROM (
        SELECT unnest(subnet_overlap_partners(p_subnet)) AS p
        UNION
        SELECT q::uuid FROM unnest(string_to_array(NULLIF(current_setting('app.subnet_overlap_taken', true), ''), ';')) e
         CROSS JOIN LATERAL unnest(string_to_array(split_part(e, ':', 2), ',')) q
         WHERE split_part(e, ':', 1) = p_subnet::text AND q <> '') x;
    PERFORM app_scope_restore(previous);
    RETURN partners;
EXCEPTION WHEN OTHERS THEN
    PERFORM app_scope_restore(previous);
    RAISE;
END
$$;

-- ─── 3. Takeover by a new, widened or moved subnet ───────────────────────────

CREATE OR REPLACE FUNCTION subnet_place() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    parent uuid;
    container uuid;
    is_strict boolean;
    partner uuid;
    partners uuid[];
    has_addresses boolean;
    source text;
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
        -- nesting and every child of a pool pass. Outside a strict section
        -- the partners are noted for the warning before the takeover below
        -- resolves the overlap.
        SELECT sec.strict_mode INTO is_strict FROM ipam_section sec
         WHERE sec.id = NEW.section_id AND sec.organization_id = NEW.organization_id;
        IF is_strict THEN
            partner := (subnet_overlap_partners(NEW.id))[1];
            IF partner IS NOT NULL THEN
                RAISE EXCEPTION 'the subnet overlaps another subnet of a strict section'
                    USING ERRCODE = '23514', CONSTRAINT = 'subnet_overlap_strict', DETAIL = partner::text;
            END IF;
        ELSE
            partners := subnet_overlap_partners(NEW.id);
            IF cardinality(partners) > 0 THEN
                PERFORM set_config('app.subnet_overlap_taken',
                    COALESCE(NULLIF(current_setting('app.subnet_overlap_taken', true), '') || ';', '')
                        || NEW.id::text || ':' || array_to_string(partners, ','), true);
            END IF;
        END IF;
        -- Reassignment (IPM-14, WP-912), recorded as allocation, only when
        -- the address space of the subnet changed: created, prefix, VRF, a
        -- pool that became a subnet. A new section changes no address.
        IF TG_OP = 'INSERT' OR OLD.cidr IS DISTINCT FROM NEW.cidr OR OLD.vrf_id IS DISTINCT FROM NEW.vrf_id
           OR (OLD.is_pool AND NOT NEW.is_pool) THEN
            source := current_setting('app.ipam_change_source', true);
            PERFORM set_config('app.ipam_change_source', 'allocation', true);
            -- Release: the addresses of the subnet outside its shrunk prefix
            -- get their longest prefix (it no longer holds them), one
            -- statement.
            IF TG_OP = 'UPDATE' AND OLD.cidr IS DISTINCT FROM NEW.cidr THEN
                UPDATE ip_address a SET subnet_id = ipam_longest_prefix_subnet(a.organization_id, a.vrf_id, a.address)
                 WHERE a.organization_id = NEW.organization_id AND a.subnet_id = NEW.id AND NOT (a.address <<= NEW.cidr);
            END IF;
            -- Takeover: the addresses of the VRF inside the subnet whose
            -- subnet is the narrowest subnet without pool around it (their
            -- longest prefix so far, ipam_longest_prefix_subnet without the
            -- subnet) or none get their longest prefix (the subnet or a
            -- child of it), one statement. A pool takes no addresses.
            IF NOT NEW.is_pool THEN
                container := (SELECT s.id FROM subnet s
                               WHERE s.organization_id = NEW.organization_id AND s.vrf_id = NEW.vrf_id AND s.id <> NEW.id
                                 AND NOT s.is_pool AND s.cidr >> NEW.cidr
                               ORDER BY masklen(s.cidr) DESC, s.id
                               LIMIT 1);
                UPDATE ip_address a SET subnet_id = n.subnet
                  FROM (SELECT x.id, ipam_longest_prefix_subnet(x.organization_id, x.vrf_id, x.address) AS subnet
                          FROM ip_address x
                         WHERE x.organization_id = NEW.organization_id AND x.vrf_id = NEW.vrf_id
                           AND x.address <<= NEW.cidr AND NOT x.is_gateway
                           AND (x.subnet_id IS NULL OR x.subnet_id IS NOT DISTINCT FROM container)) n
                 WHERE a.id = n.id AND a.organization_id = NEW.organization_id
                   AND n.subnet IS NOT NULL AND a.subnet_id IS DISTINCT FROM n.subnet;
            END IF;
            PERFORM set_config('app.ipam_change_source', COALESCE(source, ''), true);
        END IF;
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
    RETURN NULL;
END
$$;

-- The release above replaces the refusal of a shrunk prefix (000299).
DROP TRIGGER trg_subnet_cidr_keeps_addresses ON subnet;
DROP FUNCTION subnet_cidr_keeps_addresses();

-- ─── 4. Release on delete ────────────────────────────────────────────────────

CREATE FUNCTION subnet_release_addresses() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    source text;
BEGIN
    -- The deletion of the organization removes the addresses as well.
    IF NOT EXISTS (SELECT 1 FROM organization o WHERE o.id = OLD.organization_id) THEN
        RETURN OLD;
    END IF;
    PERFORM subnet_tree_lock(OLD.organization_id, OLD.vrf_id);
    previous := app_scope_widen();
    source := current_setting('app.ipam_change_source', true);
    BEGIN
        PERFORM set_config('app.ipam_change_source', 'allocation', true);
        UPDATE ip_address a SET subnet_id = (
            SELECT s.id FROM subnet s
             WHERE s.organization_id = a.organization_id AND s.vrf_id = a.vrf_id AND s.id <> OLD.id
               AND NOT s.is_pool AND s.cidr >>= a.address
             ORDER BY masklen(s.cidr) DESC, s.id
             LIMIT 1)
         WHERE a.organization_id = OLD.organization_id AND a.subnet_id = OLD.id;
    EXCEPTION WHEN OTHERS THEN
        PERFORM set_config('app.ipam_change_source', COALESCE(source, ''), true);
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM set_config('app.ipam_change_source', COALESCE(source, ''), true);
    PERFORM app_scope_restore(previous);
    RETURN OLD;
END
$$;

CREATE TRIGGER trg_subnet_release_addresses BEFORE DELETE ON subnet
    FOR EACH ROW EXECUTE FUNCTION subnet_release_addresses();

-- ─── 5. Existing addresses ───────────────────────────────────────────────────

-- Once, set-based, recorded as allocation; the derived columns (scope,
-- section) follow through their triggers, updated_at stays.
SELECT set_config('app.ipam_change_source', 'allocation', true);
ALTER TABLE ip_address DISABLE TRIGGER trg_ip_address_updated_at;
UPDATE ip_address a SET subnet_id = n.subnet
  FROM (SELECT x.id, ipam_longest_prefix_subnet(x.organization_id, x.vrf_id, x.address) AS subnet
          FROM ip_address x WHERE x.subnet_id IS NULL) n
 WHERE a.id = n.id AND n.subnet IS NOT NULL;
ALTER TABLE ip_address ENABLE TRIGGER trg_ip_address_updated_at;
SELECT set_config('app.ipam_change_source', '', true);
