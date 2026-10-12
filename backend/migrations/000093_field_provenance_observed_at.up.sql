-- WP-084 (GLO-12, REC-05): the provenance of a field keeps the source, its
-- rank and the source time of the observation. discovered_at stays the time
-- the value was recorded; write decisions compare observed_at.
ALTER TABLE ci_field_value
    ADD COLUMN discovered_rank smallint,
    ADD COLUMN observed_at timestamptz,
    ADD CONSTRAINT ci_field_value_discovered_rank CHECK (discovered_rank BETWEEN 0 AND 100);

-- Existing rows: the recording time is the best known source time; ranks
-- follow the REC-03 table (override.SourceRanks, IPMI as Redfish, E-32).
UPDATE ci_field_value SET
    observed_at = discovered_at,
    discovered_rank = CASE discovered_source
        WHEN 'manual' THEN 100 WHEN 'override' THEN 100 WHEN 'manual_override' THEN 100
        WHEN 'import' THEN 95 WHEN 'workflow' THEN 92
        WHEN 'redfish' THEN 90 WHEN 'ipmi' THEN 90 WHEN 'agent' THEN 85
        WHEN 'snmp' THEN 80 WHEN 'api' THEN 75 WHEN 'integration' THEN 75
        WHEN 'wmi' THEN 70 WHEN 'ssh' THEN 60 WHEN 'nas' THEN 60 WHEN 'sweep' THEN 20
        ELSE 0 END
WHERE discovered_source IS NOT NULL;

COMMENT ON COLUMN ci_field_value.discovered_rank IS 'REC-03 rank of discovered_source when the value was recorded';
COMMENT ON COLUMN ci_field_value.observed_at IS 'Source time of the discovered value (GLO-12)';
