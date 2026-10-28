ALTER TABLE network_interface
    DROP CONSTRAINT IF EXISTS network_interface_origin_check,
    DROP COLUMN IF EXISTS origin;

DROP INDEX IF EXISTS idx_ci_serial_number_lc;
ALTER TABLE ci DROP COLUMN IF EXISTS serial_number_lc;
