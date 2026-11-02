-- Migration 000195: the override index of ci_field_value (WP-840, NFR-02,
-- ABN-02).
--
-- The node read of the topology and the impact analysis (ci.ListNodes)
-- reads the overrides of the projected fields of the whole organization
-- once a walk reaches more than 2,000 CIs (nodeOverridesByOrg:
-- organization_id = $1 AND field_name = ANY($2) AND override_at IS NOT
-- NULL). ci_field_value had only idx_ci_field_value_ci (ci_id, 000055); in
-- an organization filled by discovery most of its rows hold discovered
-- values without an override. The partial index holds the overrides
-- alone, by organization and CI. Its columns and its predicate need no
-- function, so the application role uses it under RLS (SR-04).
--
-- Like 000109, IF NOT EXISTS with a fixed name lets an operator build the
-- index beforehand without long locks (docs/schema-baseline.md, "Indizes
-- ohne lange Sperren").

CREATE INDEX IF NOT EXISTS idx_ci_field_value_override ON ci_field_value (organization_id, ci_id) WHERE override_at IS NOT NULL;
