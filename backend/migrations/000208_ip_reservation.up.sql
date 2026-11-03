-- IP reservations of a subnet (NET-09, WP-299): a reservation is a row of
-- ip_address with type 'reserved', a subnet and no interface. reserved_until
-- is informative (no automatic release); reserved_by is the surrogate id of
-- the reserving user (convention 16, like ci_change.actor_id) without a
-- foreign key. The note lives in the existing column description. Row level
-- security and the policies of ip_address stay unchanged; no new index.
ALTER TABLE ip_address
    ADD COLUMN reserved_until timestamptz,
    ADD COLUMN reserved_by uuid;
