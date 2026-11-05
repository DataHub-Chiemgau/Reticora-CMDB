-- Migration 000263: LCY-03 as system guards (WP-279, E-120).
--
-- The global system model physical_asset (000055, global since 000056)
-- required the free context values storage_location (in_stock),
-- deployment_location (deployed) and disposal_date/disposal_record
-- (disposed). The system guards of backend/internal/lifecycle/guards.go
-- replace them: they check location, holder and disposal record against
-- the database for every asset model. The required context fields of these
-- three global states go away; models of organizations stay unchanged.
-- Global rows are read-only for the application since 000059, so only a
-- migration changes them.

UPDATE lifecycle_state
SET required_fields = '[]'::jsonb
WHERE definition_id = '00000000-0000-0000-0000-000000000101'
  AND organization_id IS NULL
  AND key IN ('in_stock', 'deployed', 'disposed');
