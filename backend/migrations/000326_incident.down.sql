-- Reverts 000326: first the foreign key on alarm, then incident_transition
-- and incident with their triggers and functions.

ALTER TABLE alarm DROP CONSTRAINT alarm_incident_fkey;
DROP INDEX idx_alarm_incident;
DROP TRIGGER IF EXISTS incident_scope_propagation ON ci;
DROP TABLE incident_transition;
DROP TABLE incident;

DROP FUNCTION propagate_incident_transition_scope();
DROP FUNCTION propagate_incident_scope();
DROP FUNCTION reject_incident_transition_mutation();
DROP FUNCTION incident_transition_scope();
DROP FUNCTION reject_incident_delete();
DROP FUNCTION incident_scope();
