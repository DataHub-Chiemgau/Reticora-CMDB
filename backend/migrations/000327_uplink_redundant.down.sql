-- Reverts 000327: the system type uplink_to is no longer redundant.

UPDATE relationship_type SET redundant = false WHERE organization_id IS NULL AND key = 'uplink_to';
