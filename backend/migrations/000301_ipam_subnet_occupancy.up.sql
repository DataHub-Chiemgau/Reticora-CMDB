-- Migration 000301: occupancy of a subnet (IPM-03, WP-903).
--
-- ipam_subnet_occupancy(p_subnet) feeds usage, free addresses and free
-- subnets. Addresses of a subnet may belong to another client than the
-- subnet (ip_address.client_id follows the interface), so a client principal
-- would see an occupied address as free under its row level security. The
-- function therefore first checks under the caller's row level security
-- that the subnet is visible (no_data_found otherwise, 404 at the API) and
-- then reads, with the scope widened (000139), only address_host, type and
-- state of the subnet's addresses (kind address) and the cidr of its direct
-- children (kind child). No other field of an invisible row leaves the
-- function. The scope is restored on every path, errors included.

CREATE FUNCTION ipam_subnet_occupancy(p_subnet uuid)
RETURNS TABLE (kind text, value text, ip_type text, ip_state text)
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    previous text[];
    org uuid;
    vrf uuid;
BEGIN
    SELECT s.organization_id, s.vrf_id INTO org, vrf FROM subnet s WHERE s.id = p_subnet;
    IF org IS NULL THEN
        RAISE EXCEPTION 'subnet not found' USING ERRCODE = 'no_data_found';
    END IF;
    previous := app_scope_widen();
    BEGIN
        RETURN QUERY
            SELECT 'address'::text, a.address_host, a.type, a.state
              FROM ip_address a
             WHERE a.organization_id = org AND a.subnet_id = p_subnet
            UNION ALL
            SELECT 'child'::text, c.cidr::text, NULL::text, NULL::text
              FROM subnet c
             WHERE c.organization_id = org AND c.vrf_id = vrf AND c.parent_id = p_subnet;
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
END
$$;
