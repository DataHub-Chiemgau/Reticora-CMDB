-- Reverts 000299: the gateway synchronisation, the gateway and prefix
-- checks of subnets and addresses, the gateway index and the
-- columns state, state_changed_at and is_gateway go, and the outbox accepts
-- two-segment event types only again. The revert is refused while the
-- outbox holds an event type of more segments.
DROP FUNCTION ipam_subnet_sync_gateway(uuid, uuid, inet);
ALTER TABLE ip_address DROP CONSTRAINT ip_address_gateway_has_subnet;
DROP TRIGGER trg_ip_address_gateway_leaves_subnet ON ip_address;
DROP FUNCTION ip_address_gateway_leaves_subnet();
DROP TRIGGER trg_subnet_gateway_in_cidr ON subnet;
DROP FUNCTION subnet_gateway_in_cidr();
DROP TRIGGER trg_subnet_cidr_keeps_addresses ON subnet;
DROP FUNCTION subnet_cidr_keeps_addresses();
DROP INDEX ip_address_one_gateway;
ALTER TABLE ip_address
    DROP COLUMN is_gateway,
    DROP COLUMN state_changed_at,
    DROP COLUMN state;

DO $$
BEGIN
    ALTER TABLE event_outbox DROP CONSTRAINT event_outbox_type_check;
    ALTER TABLE event_outbox ADD CONSTRAINT event_outbox_type_check CHECK (type ~ '^[a-z_]+\.[a-z_]+$');
EXCEPTION WHEN check_violation THEN
    RAISE EXCEPTION 'event_outbox holds event types of three segments; migration 000299 cannot be reverted'
        USING ERRCODE = 'check_violation';
END
$$;
