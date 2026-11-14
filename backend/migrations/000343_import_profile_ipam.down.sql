-- The profiles of the IPAM entities go with them; the CHECK of 000219
-- comes back.
DELETE FROM import_profile WHERE entity IN ('subnet', 'ip_address');
ALTER TABLE import_profile DROP CONSTRAINT import_profile_entity_check;
ALTER TABLE import_profile ADD CONSTRAINT import_profile_entity_check
    CHECK (entity IN ('ci', 'asset', 'contract', 'consumable', 'location', 'relationship'));
