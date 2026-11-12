-- Migration 000299: state and gateway mark of IP addresses (IPM-04, WP-902,
-- E-211).
--
-- * ip_address.state active | offline | unknown with state_changed_at, next
--   to type (NET-03 stays the origin/purpose of the address). Backfill: an
--   address seen within the last 7 days is active (state_changed_at =
--   last_seen_at), every other one unknown. The backfill neither touches
--   updated_at nor writes events (triggers disabled as in 000111).
-- * ip_address.is_gateway marks the address that is the gateway of its
--   subnet; at most one per subnet (partial unique index). The write path of
--   the subnets keeps the mark in step with subnet.gateway.

ALTER TABLE ip_address
    ADD COLUMN state text NOT NULL DEFAULT 'unknown'
        CONSTRAINT ip_address_state_check CHECK (state IN ('active', 'offline', 'unknown')),
    ADD COLUMN state_changed_at timestamptz NULL,
    ADD COLUMN is_gateway boolean NOT NULL DEFAULT false;

ALTER TABLE ip_address DISABLE TRIGGER trg_ip_address_updated_at;
ALTER TABLE ip_address DISABLE TRIGGER ip_address_scope;
UPDATE ip_address SET state = 'active', state_changed_at = last_seen_at
 WHERE last_seen_at >= now() - interval '7 days';
ALTER TABLE ip_address ENABLE TRIGGER ip_address_scope;
ALTER TABLE ip_address ENABLE TRIGGER trg_ip_address_updated_at;

CREATE UNIQUE INDEX ip_address_one_gateway ON ip_address (subnet_id) WHERE is_gateway;

-- The event types of the network domain of phase 3 (WP-870) have three
-- segments (ipam.address.state_changed, ipam.subnet.changed,
-- admp.connection.refreshed); the type check of the outbox (000120) allowed
-- two only.
ALTER TABLE event_outbox DROP CONSTRAINT event_outbox_type_check;
ALTER TABLE event_outbox ADD CONSTRAINT event_outbox_type_check CHECK (type ~ '^[a-z_]+(\.[a-z_]+)+$');

-- PATCH /subnets/{id} may change the prefix (the gateway is then checked
-- against the new one). The addresses of the subnet, also those outside the
-- writer's scope, must stay inside it (subnet_cidr_addresses, 422 cidr);
-- they are read with the scope widened (000139), the scope is restored
-- before the check reports.
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

-- The gateway of a subnet lies inside its prefix (IPM-04, 422 gateway).
-- The handler checks the request; this trigger checks the row as the
-- update leaves it, so two concurrent PATCHes of prefix and gateway, each
-- checked against the other's old value, cannot leave the gateway outside.
-- Rows written before stay as they are until their prefix or gateway
-- changes.
CREATE FUNCTION subnet_gateway_in_cidr() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    RAISE EXCEPTION 'the gateway of the subnet lies outside its prefix'
        USING ERRCODE = '23514', CONSTRAINT = 'subnet_gateway_in_cidr';
END
$$;
CREATE TRIGGER trg_subnet_gateway_in_cidr AFTER INSERT OR UPDATE OF cidr, gateway ON subnet
    FOR EACH ROW WHEN (NEW.gateway IS NOT NULL AND NOT (NEW.gateway <<= NEW.cidr))
    EXECUTE FUNCTION subnet_gateway_in_cidr();

-- The gateway mark belongs to the subnet: an address that leaves its
-- subnet (PATCH subnet_id, or the subnet deleted: ON DELETE SET NULL of
-- ip_address_subnet_id_fkey runs as an update of subnet_id) loses it, and
-- an address without subnet carries none.
CREATE FUNCTION ip_address_gateway_leaves_subnet() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF OLD.is_gateway AND OLD.subnet_id IS DISTINCT FROM NEW.subnet_id THEN
        NEW.is_gateway := false;
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER trg_ip_address_gateway_leaves_subnet BEFORE UPDATE OF subnet_id ON ip_address
    FOR EACH ROW EXECUTE FUNCTION ip_address_gateway_leaves_subnet();
ALTER TABLE ip_address ADD CONSTRAINT ip_address_gateway_has_subnet CHECK (NOT is_gateway OR subnet_id IS NOT NULL);

-- ipam_subnet_sync_gateway keeps the gateway mark of the addresses of a
-- subnet in step with its gateway p_gateway (IPM-04), in the writing
-- transaction. The addresses of a subnet can belong to other clients than
-- the subnet (ip_address.client_id follows the interface, 000061), so the
-- mark is kept over all of them: the subnet is first read under the
-- caller's row level security (no_data_found when hidden), then the scope
-- is widened (000139) and restored on every path. The mark of another
-- address goes (the address stays); without gateway that is all. Otherwise
-- an address of the gateway in the subnet's VRF that belongs to the subnet
-- or to none is marked (and put into the subnet), the subnet's own and one
-- without interface first; without one, an address without interface of
-- type static is created in the subnet.
CREATE FUNCTION ipam_subnet_sync_gateway(p_org uuid, p_subnet uuid, p_gateway inet) RETURNS void
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    v_vrf uuid;
    previous text[];
    pick uuid;
BEGIN
    SELECT s.vrf_id INTO v_vrf FROM subnet s WHERE s.organization_id = p_org AND s.id = p_subnet;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'subnet not found' USING ERRCODE = 'P0002';
    END IF;
    previous := app_scope_widen();
    BEGIN
        UPDATE ip_address SET is_gateway = false
         WHERE organization_id = p_org AND subnet_id = p_subnet AND is_gateway
           AND (p_gateway IS NULL OR address_host IS DISTINCT FROM host(p_gateway));
        IF p_gateway IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ip_address
                WHERE organization_id = p_org AND subnet_id = p_subnet AND is_gateway) THEN
            SELECT a.id INTO pick FROM ip_address a
             WHERE a.organization_id = p_org AND a.vrf_id = v_vrf AND a.address_host = host(p_gateway)
               AND (a.subnet_id = p_subnet OR a.subnet_id IS NULL)
             ORDER BY (a.subnet_id IS NOT NULL) DESC, (a.interface_id IS NULL) DESC, a.created_at, a.id
             LIMIT 1;
            IF pick IS NOT NULL THEN
                UPDATE ip_address SET is_gateway = true, subnet_id = p_subnet, updated_at = now()
                 WHERE organization_id = p_org AND id = pick;
            ELSE
                INSERT INTO ip_address (organization_id, subnet_id, address, type, is_gateway)
                VALUES (p_org, p_subnet, p_gateway, 'static', true);
            END IF;
        END IF;
    EXCEPTION WHEN OTHERS THEN
        PERFORM app_scope_restore(previous);
        RAISE;
    END;
    PERFORM app_scope_restore(previous);
END
$$;

-- Backfill of the gateway mark (IPM-04): every subnet whose gateway lies
-- inside its prefix and that has no marked address yet gets its gateway
-- address as ipam_subnet_sync_gateway would set it, set-based: an address
-- of the gateway in the subnet's VRF that belongs to the subnet or to none
-- is marked (the subnet's own and one without interface first; an address
-- matching several subnets goes to one of them), the remaining subnets get
-- a static address without interface. The mark does not touch updated_at
-- (trigger disabled as for the state); the scope of a created or moved
-- address follows its subnet as on every write (ip_address_scope). A
-- gateway outside the prefix stays unmarked and is reported as a notice;
-- the next change of the subnet's prefix or gateway has to correct it
-- (subnet_gateway_in_cidr).
ALTER TABLE ip_address DISABLE TRIGGER trg_ip_address_updated_at;
WITH pick AS (
    SELECT DISTINCT ON (s.id) s.id AS subnet_id, a.id AS address_id
      FROM subnet s
      JOIN ip_address a ON a.organization_id = s.organization_id AND a.vrf_id = s.vrf_id
                       AND a.address_host = host(s.gateway)
                       AND (a.subnet_id = s.id OR a.subnet_id IS NULL) AND NOT a.is_gateway
     WHERE s.gateway IS NOT NULL AND s.gateway <<= s.cidr
       AND NOT EXISTS (SELECT 1 FROM ip_address m WHERE m.subnet_id = s.id AND m.is_gateway)
     ORDER BY s.id, (a.subnet_id IS NOT NULL) DESC, (a.interface_id IS NULL) DESC, a.created_at, a.id
), once AS (
    SELECT DISTINCT ON (address_id) subnet_id, address_id FROM pick ORDER BY address_id, subnet_id
)
UPDATE ip_address a SET is_gateway = true, subnet_id = once.subnet_id
  FROM once WHERE a.id = once.address_id;
ALTER TABLE ip_address ENABLE TRIGGER trg_ip_address_updated_at;
INSERT INTO ip_address (organization_id, subnet_id, address, type, is_gateway)
SELECT s.organization_id, s.id, s.gateway, 'static', true
  FROM subnet s
 WHERE s.gateway IS NOT NULL AND s.gateway <<= s.cidr
   AND NOT EXISTS (SELECT 1 FROM ip_address m WHERE m.subnet_id = s.id AND m.is_gateway);
DO $$
DECLARE
    outside bigint;
BEGIN
    SELECT count(*) INTO outside FROM subnet WHERE gateway IS NOT NULL AND NOT (gateway <<= cidr);
    IF outside > 0 THEN
        RAISE NOTICE '000299: % subnet(s) with a gateway outside the prefix stay without gateway mark', outside;
    END IF;
END
$$;
