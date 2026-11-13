-- Reverts 000359: the links to incidents and alarms are deleted, both
-- CHECKs return to their form of 000230.

DELETE FROM ticket_link WHERE entity_type IN ('incident', 'alarm');

DROP INDEX idx_ticket_link_alarm;
DROP INDEX idx_ticket_link_incident;

ALTER TABLE ticket_link
    DROP CONSTRAINT ticket_link_ticket_id_alarm_id_key,
    DROP CONSTRAINT ticket_link_ticket_id_incident_id_key,
    DROP CONSTRAINT ticket_link_alarm_fkey,
    DROP CONSTRAINT ticket_link_incident_fkey,
    DROP CONSTRAINT ticket_link_target_check,
    DROP CONSTRAINT ticket_link_entity_type_check;

ALTER TABLE ticket_link
    ADD CONSTRAINT ticket_link_entity_type_check
        CHECK (entity_type IN ('ci', 'asset', 'location', 'contract', 'review_item')),
    ADD CONSTRAINT ticket_link_target_check CHECK (
        num_nonnulls(ci_id, asset_id, location_id, contract_id, review_item_id) = 1
        AND CASE entity_type
            WHEN 'ci' THEN ci_id IS NOT NULL
            WHEN 'asset' THEN asset_id IS NOT NULL
            WHEN 'location' THEN location_id IS NOT NULL
            WHEN 'contract' THEN contract_id IS NOT NULL
            WHEN 'review_item' THEN review_item_id IS NOT NULL
            ELSE false
        END),
    DROP COLUMN alarm_id,
    DROP COLUMN incident_id;
