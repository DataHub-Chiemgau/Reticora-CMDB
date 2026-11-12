-- Reverts 000306: the table vrf_section, the index and the columns of vrf go.
DROP TABLE vrf_section;
DROP INDEX vrf_route_distinguisher_key;
ALTER TABLE vrf
    DROP CONSTRAINT vrf_default_no_routing,
    DROP CONSTRAINT vrf_route_targets_check,
    DROP CONSTRAINT vrf_route_distinguisher_check,
    DROP COLUMN export_targets,
    DROP COLUMN import_targets,
    DROP COLUMN route_distinguisher;
