-- WP-920 (IPM-09): the file import takes subnets and IP addresses. The
-- entities subnet and ip_address join the CHECK of import_profile.entity;
-- no new table, RLS and policies of import_profile (000219) stay.
ALTER TABLE import_profile DROP CONSTRAINT import_profile_entity_check;
ALTER TABLE import_profile ADD CONSTRAINT import_profile_entity_check
    CHECK (entity IN ('ci', 'asset', 'contract', 'consumable', 'location', 'relationship', 'subnet', 'ip_address'));
