-- WP-1026 (ALR-03, ALR-09, E-199 option 1): the system type uplink_to is
-- redundant. A CPE with two uplinks fails only when all its uplink
-- providers fail (certainDown); with one uplink the result is the same as
-- before. The types of the organizations stay unchanged.

UPDATE relationship_type SET redundant = true WHERE organization_id IS NULL AND key = 'uplink_to';
