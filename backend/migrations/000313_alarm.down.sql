-- Reverts 000313: alarm_transition and alarm with their triggers and
-- functions.

DROP TRIGGER IF EXISTS alarm_scope_propagation ON ci;
DROP TABLE alarm_transition;
DROP TABLE alarm;

DROP FUNCTION propagate_alarm_transition_scope();
DROP FUNCTION propagate_alarm_scope();
DROP FUNCTION reject_alarm_transition_mutation();
DROP FUNCTION alarm_transition_scope();
DROP FUNCTION reject_alarm_delete();
DROP FUNCTION alarm_scope();
