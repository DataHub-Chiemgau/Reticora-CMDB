DROP TRIGGER IF EXISTS trg_composition_no_cycle ON composition;
DROP FUNCTION IF EXISTS reject_composition_cycle();
