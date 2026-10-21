-- Reverts WP-396: the type of a CI is checked by the single-column foreign
-- key ci_ci_type_id_fkey only.

DROP TRIGGER trg_ci_type_organization ON ci;
DROP FUNCTION ci_check_type_organization();
