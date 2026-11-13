-- WP-1039 (ALR-09 d, TKT-01 Monitoring [P3]): a ticket links to an incident
-- (WP-1027) or an alarm (WP-981), so the open tickets of an incident are
-- found and linked instead of a second ticket (ticket dedupe of WP-1040).
--
-- 1. incident_id and alarm_id with composite foreign keys on
--    (id, organization_id) (Konvention 19): deleting the incident or the
--    alarm deletes its links.
-- 2. ticket_link_entity_type_check and ticket_link_target_check take the
--    types incident and alarm (num_nonnulls over seven target columns).
-- 3. One link per ticket and incident or alarm; partial indexes for the
--    lookups by target.
--
-- The scope columns follow the ticket (derive_ticket_link_scope); policies,
-- FORCE RLS and the truncate guard of 000230 stay unchanged.

ALTER TABLE ticket_link
    ADD COLUMN incident_id UUID,
    ADD COLUMN alarm_id UUID,
    DROP CONSTRAINT ticket_link_entity_type_check,
    DROP CONSTRAINT ticket_link_target_check;

ALTER TABLE ticket_link
    ADD CONSTRAINT ticket_link_entity_type_check
        CHECK (entity_type IN ('ci', 'asset', 'location', 'contract', 'review_item', 'incident', 'alarm')),
    ADD CONSTRAINT ticket_link_target_check CHECK (
        num_nonnulls(ci_id, asset_id, location_id, contract_id, review_item_id, incident_id, alarm_id) = 1
        AND CASE entity_type
            WHEN 'ci' THEN ci_id IS NOT NULL
            WHEN 'asset' THEN asset_id IS NOT NULL
            WHEN 'location' THEN location_id IS NOT NULL
            WHEN 'contract' THEN contract_id IS NOT NULL
            WHEN 'review_item' THEN review_item_id IS NOT NULL
            WHEN 'incident' THEN incident_id IS NOT NULL
            WHEN 'alarm' THEN alarm_id IS NOT NULL
            ELSE false
        END),
    ADD CONSTRAINT ticket_link_incident_fkey FOREIGN KEY (incident_id, organization_id)
        REFERENCES incident (id, organization_id) ON DELETE CASCADE,
    ADD CONSTRAINT ticket_link_alarm_fkey FOREIGN KEY (alarm_id, organization_id)
        REFERENCES alarm (id, organization_id) ON DELETE CASCADE,
    ADD CONSTRAINT ticket_link_ticket_id_incident_id_key UNIQUE (ticket_id, incident_id),
    ADD CONSTRAINT ticket_link_ticket_id_alarm_id_key UNIQUE (ticket_id, alarm_id);

CREATE INDEX idx_ticket_link_incident ON ticket_link (incident_id) WHERE incident_id IS NOT NULL;
CREATE INDEX idx_ticket_link_alarm ON ticket_link (alarm_id) WHERE alarm_id IS NOT NULL;
