ALTER TABLE ci_field_value
    DROP CONSTRAINT ci_field_value_discovered_rank,
    DROP COLUMN observed_at,
    DROP COLUMN discovered_rank;
