-- Reverts 000350: the availability targets go, with the derivation of
-- their scope and its propagation on a CI move.
DROP TRIGGER trg_ci_availability_target_scope ON ci;
DROP FUNCTION propagate_availability_target_scope();
DROP TABLE availability_target;
DROP FUNCTION availability_target_ci_scope();
